package git

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"golang.org/x/crypto/openpgp"
	"gopkg.in/src-d/go-git.v4/plumbing"
	"gopkg.in/src-d/go-git.v4/plumbing/filemode"
	"gopkg.in/src-d/go-git.v4/plumbing/format/index"
	"gopkg.in/src-d/go-git.v4/plumbing/object"
	"gopkg.in/src-d/go-git.v4/storage"

	"gopkg.in/src-d/go-billy.v4"
)

const hasUnmergedFilesMSG = `error: commit is not possible because you have unmerged files.
hint: Fix them up in the work tree, and then use 'git add/rm <file>'
hint: as appropriate to mark resolution and make a commit.
fatal: Exiting because of an unresolved conflict.`

var ErrHasUnmergedFiles = errors.New(hasUnmergedFilesMSG)

// Commit stores the current contents of the index in a new commit along with
// a log message from the user describing the changes.
func (w *Worktree) Commit(msg string, opts *CommitOptions) (plumbing.Hash, error) {

	mh, err := w.r.MergeHead()
	if err != nil {
		return plumbing.ZeroHash, err
	}

	if mh != nil {
		head, err := w.r.Head()
		if err != nil && err != plumbing.ErrReferenceNotFound {
			return plumbing.ZeroHash, err
		}

		if head != nil {
			opts.Parents = []plumbing.Hash{head.Hash()}
		}

		opts.Parents = append(opts.Parents, mh.Hash())

		if msg == "" {
			m, err := w.MergeMsg()

			if err != nil {
				return plumbing.ZeroHash, err
			}

			msg = m
		}
	}

	if err := opts.Validate(w.r); err != nil {
		return plumbing.ZeroHash, err
	}

	unmerged, err := w.getUnmergedFiles()
	if err != nil {
		w.printUnmergedFilesError(unmerged)

		return plumbing.ZeroHash, err
	}

	if len(unmerged) != 0 {

		return plumbing.ZeroHash, ErrHasUnmergedFiles
	}

	if opts.All {
		if err := w.autoAddModifiedAndDeleted(); err != nil {
			return plumbing.ZeroHash, err
		}
	}

	idx, err := w.r.Storer.Index()
	if err != nil {
		return plumbing.ZeroHash, err
	}

	h := &buildTreeHelper{
		fs: w.Filesystem,
		s:  w.r.Storer,
	}

	tree, err := h.BuildTree(idx)
	if err != nil {
		return plumbing.ZeroHash, err
	}

	commit, err := w.buildCommitObject(msg, opts, tree)
	if err != nil {
		return plumbing.ZeroHash, err
	}

	err = w.updateHEAD(commit)
	if err != nil {
		return commit, err
	}

	err = w.removeMergeHead()
	if err != nil {
		return commit, err
	}

	w.r.Storer.RemoveMergeMsg()
	w.removeOrigHead()
	w.blobs = nil

	return commit, nil
}

func (w *Worktree) getUnmergedFiles() (map[string][]index.Stage, error) {
	idx, err := w.r.Storer.Index()
	if err != nil {
		return nil, err
	}

	unmerged := map[string][]index.Stage{}
	for _, entry := range idx.Entries {
		if entry.Stage != index.Merged {
			stages, ok := unmerged[entry.Name]

			if ok {
				stages = append(stages, entry.Stage)
			} else {
				unmerged[entry.Name] = []index.Stage{entry.Stage}
			}
		}
	}

	return unmerged, nil
}

func (w *Worktree) printUnmergedFilesError(unmergedFiles map[string][]index.Stage) {
	if unmergedFiles == nil || len(unmergedFiles) == 0 {
		return
	}

	var b strings.Builder

	for path := range unmergedFiles {
		fmt.Fprintf(&b, "U	%s\n", path)
	}

	b.WriteString(hasUnmergedFilesMSG)

	fmt.Fprintf(os.Stdout, b.String())
}

func (w *Worktree) removeMergeHead() error {
	mh, err := w.r.MergeHead()

	if err != nil {
		return err
	}

	if mh != nil {
		err = w.r.Storer.RemoveReference(plumbing.MERGE_HEAD)
		if err != nil {
			return err
		}
	}

	return nil
}

func (w *Worktree) removeOrigHead() error {
	head, err := w.r.OrigHead()

	if err != nil {
		if err == plumbing.ErrReferenceNotFound {
			return nil
		}

		return err
	}

	if head != nil {
		err = w.r.Storer.RemoveReference(plumbing.ORIG_HEAD)
		if err != nil {
			return err
		}
	}

	return nil
}

func (w *Worktree) autoAddModifiedAndDeleted() error {
	s, err := w.Status()
	if err != nil {
		return err
	}

	for path, fs := range s {
		if fs.Worktree != Modified && fs.Worktree != Deleted {
			continue
		}

		if err := w.Add(path); err != nil {
			return err
		}
	}

	return nil
}

func (w *Worktree) updateHEAD(commit plumbing.Hash) error {
	head, err := w.r.Storer.Reference(plumbing.HEAD)
	if err != nil {
		return err
	}

	name := plumbing.HEAD
	if head.Type() != plumbing.HashReference {
		name = head.Target()
	}

	ref := plumbing.NewHashReference(name, commit)
	return w.r.Storer.SetReference(ref)
}

func (w *Worktree) buildCommitObject(msg string, opts *CommitOptions, tree plumbing.Hash) (plumbing.Hash, error) {
	commit := &object.Commit{
		Author:       *opts.Author,
		Committer:    *opts.Committer,
		Message:      msg,
		TreeHash:     tree,
		ParentHashes: opts.Parents,
	}

	if opts.SignKey != nil {
		sig, err := w.buildCommitSignature(commit, opts.SignKey)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		commit.PGPSignature = sig
	}

	obj := w.r.Storer.NewEncodedObject()
	if err := commit.Encode(obj); err != nil {
		return plumbing.ZeroHash, err
	}
	return w.r.Storer.SetEncodedObject(obj)
}

func (w *Worktree) buildCommitSignature(commit *object.Commit, signKey *openpgp.Entity) (string, error) {
	encoded := &plumbing.MemoryObject{}
	if err := commit.Encode(encoded); err != nil {
		return "", err
	}
	r, err := encoded.Reader()
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&b, signKey, r, nil); err != nil {
		return "", err
	}
	return b.String(), nil
}

// buildTreeHelper converts a given index.Index file into multiple git objects
// reading the blobs from the given filesystem and creating the trees from the
// index structure. The created objects are pushed to a given Storer.
type buildTreeHelper struct {
	fs billy.Filesystem
	s  storage.Storer

	trees   map[string]*object.Tree
	entries map[string]*object.TreeEntry
}

// BuildTree builds the tree objects and push its to the storer, the hash
// of the root tree is returned.
func (h *buildTreeHelper) BuildTree(idx *index.Index) (plumbing.Hash, error) {
	const rootNode = ""
	h.trees = map[string]*object.Tree{rootNode: {}}
	h.entries = map[string]*object.TreeEntry{}

	for _, e := range idx.Entries {
		if err := h.commitIndexEntry(e); err != nil {
			return plumbing.ZeroHash, err
		}
	}

	return h.copyTreeToStorageRecursive(rootNode, h.trees[rootNode])
}

func (h *buildTreeHelper) commitIndexEntry(e *index.Entry) error {
	parts := strings.Split(e.Name, "/")

	var fullpath string
	for _, part := range parts {
		parent := fullpath
		fullpath = path.Join(fullpath, part)

		h.doBuildTree(e, parent, fullpath)
	}

	return nil
}

func (h *buildTreeHelper) doBuildTree(e *index.Entry, parent, fullpath string) {
	if _, ok := h.trees[fullpath]; ok {
		return
	}

	if _, ok := h.entries[fullpath]; ok {
		return
	}

	te := object.TreeEntry{Name: path.Base(fullpath)}

	if fullpath == e.Name {
		te.Mode = e.Mode
		te.Hash = e.Hash
	} else {
		te.Mode = filemode.Dir
		h.trees[fullpath] = &object.Tree{}
	}

	h.trees[parent].Entries = append(h.trees[parent].Entries, te)
}

type sortableEntries []object.TreeEntry

func (sortableEntries) sortName(te object.TreeEntry) string {
	if te.Mode == filemode.Dir {
		return te.Name + "/"
	}
	return te.Name
}
func (se sortableEntries) Len() int               { return len(se) }
func (se sortableEntries) Less(i int, j int) bool { return se.sortName(se[i]) < se.sortName(se[j]) }
func (se sortableEntries) Swap(i int, j int)      { se[i], se[j] = se[j], se[i] }

func (h *buildTreeHelper) copyTreeToStorageRecursive(parent string, t *object.Tree) (plumbing.Hash, error) {
	sort.Sort(sortableEntries(t.Entries))
	for i, e := range t.Entries {
		if e.Mode != filemode.Dir && !e.Hash.IsZero() {
			continue
		}

		path := path.Join(parent, e.Name)

		var err error
		e.Hash, err = h.copyTreeToStorageRecursive(path, h.trees[path])
		if err != nil {
			return plumbing.ZeroHash, err
		}

		t.Entries[i] = e
	}

	o := h.s.NewEncodedObject()
	if err := t.Encode(o); err != nil {
		return plumbing.ZeroHash, err
	}

	return h.s.SetEncodedObject(o)
}
