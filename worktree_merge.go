package git

import (
	"bufio"
	"container/heap"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"strings"

	"gopkg.in/src-d/go-git.v4/plumbing"
	"gopkg.in/src-d/go-git.v4/plumbing/format/index"
	"gopkg.in/src-d/go-git.v4/plumbing/object"
	"gopkg.in/src-d/go-git.v4/utils/merkletrie"
	mindex "gopkg.in/src-d/go-git.v4/utils/merkletrie/index"
	"gopkg.in/src-d/go-git.v4/utils/merkletrie/noder"
)

var (
	//ErrMergeInProgress occurs when thre is a MERGE_HEAD in fs, it tells that merge is in progress
	ErrMergeInProgress = errors.New("fatal: You have not concluded your merge (MERGE_HEAD exists). Please, commit your changes before you merge.")

	//ErrMergeCommitNeeded occurs when merge was without conflicts and coomit is needed to finish it
	ErrMergeCommitNeeded = errors.New("Create merge commit to continue merge process")

	//ErrMergeWithConflicts occurs when merge was with conflicts
	ErrMergeWithConflicts = errors.New("Automatic merge failed; fix conflicts and then commit the result")

	//ErrHasUncommittedFiles occurs when there are any unstaged or staged files before merge
	ErrHasUncommittedFiles = errors.New(`error: Your local changes to the files would be overwritten by merge.
Please commit your changes or stash them before you merge.
Aborting`)
)

var (
	delimStart  = []byte("<<<<<<< yours\n")
	delimMiddle = []byte("=======\n")
	delimEnd    = []byte(">>>>>>> theirs\n")
)

type mergingCommit struct {
	commit    *object.Commit
	idx       *index.Index
	isVirtual bool
}

type blobInfo struct {
	path  string
	stage index.Stage
	blob  *object.Blob
}

// Merge - analog of git merge but without flags and options
// returns ErrMergeCommitNeeded (if no conflicts) or ErrMergeWithConflicts (if there were conflicts)
// or error if it's occurs
//also returns merge message if it's necessary
func (w *Worktree) Merge(theirsBranch string) (string, error) {
	head, err := w.r.Head()
	if err != nil {
		return "", err
	}

	if head == nil {
		return "", ErrHeadNotFound
	}

	oursHash := head.Hash()

	theirsRefName := plumbing.NewBranchReferenceName(theirsBranch)
	theirsRef, err := w.r.Storer.Reference(theirsRefName)
	if err != nil {
		return "", err
	}
	theirsHash := theirsRef.Hash()

	ff, err := isFastForward(w.r.Storer, oursHash, theirsHash)
	if err != nil {
		return "", err
	}

	if ff {
		if err := w.updateHEAD(theirsHash); err != nil {
			return "", err
		}

		err = w.Reset(&ResetOptions{Commit: theirsHash, Mode: HardReset})
		if err != nil {
			return "", err
		}

	} else {
		mergeMsg, err := w.nonFastForwardMerge(oursHash, theirsHash, theirsRefName)

		if err != nil {
			return mergeMsg, err
		}
	}

	return "", nil
}

//MergeMsg - returns message from MERGE_MSG file
func (w *Worktree) MergeMsg() (string, error) {
	msg, err := w.r.Storer.MergeMsg()

	if err != nil {
		return "", nil
	}

	return msg, nil
}

//MergeMsgFileContent returns MERGE_MSG content without trimming strings which begin from "#"
func (w *Worktree) MergeMsgFileContent() (string, error) {
	msg, err := w.r.Storer.MergeMsgFileContent()

	if err != nil {
		return "", nil
	}

	return msg, nil
}

//ConflictEntries returns entries from Index with stages not equal index.Merged
//the key of map is path of files in the tree
//it can be several entries with the same path and different stage values (base, ours, theirs)
func (w *Worktree) ConflictEntries() (map[string][]*index.Entry, error) {
	idx, err := w.Index()
	if err != nil {
		return nil, err
	}

	withConf := map[string][]*index.Entry{}

	for _, e := range idx.Entries {

		if e.Stage != index.Merged {
			c, ok := withConf[e.Name]

			if ok {
				c = append(c, e)
				withConf[e.Name] = c
			} else {
				withConf[e.Name] = []*index.Entry{e}
			}
		}
	}

	return withConf, nil
}

//ReadFileByStage returns io.Reader for base, ours or theirs file depending on stage
//or object.ErrFileNotFound if there is no such file
func (w *Worktree) ReadFileByStage(path string, st index.Stage) (io.Reader, error) {

	if st == index.Merged {
		f, err := w.Filesystem.Open(path)
		if err != nil {
			if err == os.ErrNotExist {
				return nil, object.ErrFileNotFound
			}

			return nil, err
		}

		return f, nil
	}

	if w.blobs == nil {
		return nil, object.ErrFileNotFound
	}

	blobs, ok := w.blobs[path]
	if !ok {
		return nil, object.ErrFileNotFound
	}

	for _, b := range blobs {
		if b.stage == st {
			r, err := b.blob.Reader()
			if err != nil {
				return nil, err
			}

			return r, nil
		}
	}

	return nil, object.ErrFileNotFound
}

func (w *Worktree) nonFastForwardMerge(ours, theirs plumbing.Hash, theirsBranch plumbing.ReferenceName) (string, error) {
	s := w.r.Storer
	mh, err := w.r.MergeHead()

	if err != nil {
		return "", err
	}

	if mh != nil {
		return "", ErrMergeInProgress
	}

	hasUncommittedFiles, err := w.hasUncommittedFiles(ours)
	if hasUncommittedFiles {
		return "", ErrHasUncommittedFiles
	}

	oursC, err := object.GetCommit(s, ours)
	if err != nil {
		return "", err
	}

	theirsC, err := object.GetCommit(s, theirs)
	if err != nil {
		return "", err
	}

	p, err := w.computeParent(oursC, theirsC)

	if err != nil {
		return "", err
	}

	mergeHead := plumbing.NewHashReference(plumbing.MERGE_HEAD, theirs) //set merge head
	err = w.r.Storer.SetReference(mergeHead)

	if err != nil {
		return "", err
	}

	origHead := plumbing.NewHashReference(plumbing.ORIG_HEAD, ours) //set orig_head
	err = w.r.Storer.SetReference(origHead)

	if err != nil {
		return "", err
	}

	_, mergeRes, err := w.mergeCommits(p, &mergingCommit{commit: oursC}, &mergingCommit{commit: theirsC}, 0)

	if err != nil {
		return "", err
	}

	hasMergeConflicts, mergeMsg, err := w.logMergeConflicts(mergeRes, theirsBranch)

	if err != nil {
		return "", err
	}

	if hasMergeConflicts {
		return mergeMsg, ErrMergeWithConflicts
	}

	return mergeMsg, ErrMergeCommitNeeded
}

func (w *Worktree) hasUncommittedFiles(commit plumbing.Hash) (bool, error) {
	stagedChanges, err := w.diffCommitWithStaging(commit, false)
	if err != nil {
		return false, err
	}

	if len(stagedChanges) != 0 {
		return true, nil
	}

	unstagedChanges, err := w.diffStagingWithWorktree(false)
	if err != nil {
		return false, err
	}

	withoutDel := []merkletrie.Change{}
	for _, c := range unstagedChanges {

		a, err := c.Action()
		if err != nil {
			return false, err
		}

		if a != merkletrie.Delete {
			withoutDel = append(withoutDel, c)
		}
	}

	if len(withoutDel) != 0 {
		return true, nil
	}

	return false, nil
}

func (w *Worktree) logMergeConflicts(mergeResult map[string]*mergingResult, theirsBranch plumbing.ReferenceName) (hasConflicts bool, mergeMsg string, err error) {
	theirs := theirsBranch.Short()

	//builder for merge message
	var b strings.Builder

	//builder for writing in MERGE_MSG file
	var fb strings.Builder
	fb.WriteString(fmt.Sprintf("Merge branch '%s'\n\n", theirs))
	fb.WriteString("# Conflicts:\n")

	for path, r := range mergeResult {
		switch r.diffType {
		case mergeDiffBothAdded:
			{
				hasConflicts = true

				fmt.Fprintf(&b, "Auto-merging %s\n", path)
				fmt.Fprintf(&b, "CONFLICT (add/add): Merge conflict in %s\n", path)

				fmt.Fprintf(&fb, "#	%s\n", path)
			}
		case mergeDiffBothDeleted:
			{
				continue
			}
		case mergeDiffBothModifiedWithConflicts:
			{
				hasConflicts = true

				fmt.Fprintf(&b, "Auto-merging %s\n", path)
				fmt.Fprintf(&b, "CONFLICT (content): Merge conflict in %s\n", path)

				fmt.Fprintf(&fb, "#	%s\n", path)
			}
		case mergeDiffBothModifiedWithoutConflicts:
			{
				continue
			}
		case mergeDiffModifiedDeleted:
			{
				hasConflicts = true

				fmt.Fprintf(&b, "(modify/delete): %s modified in HEAD and deleted in %s.\n", path, theirs)

				fmt.Fprintf(&fb, "#	%s\n", path)
			}
		case mergeDiffDeletedModified:
			{
				hasConflicts = true

				fmt.Fprintf(&b, "(delete/modify): %s deleted in HEAD and modified in %s.\n", path, theirs)

				fmt.Fprintf(&fb, "#	%s\n", path)
			}
		case mergeDiffNoConflict:
			{
				continue
			}
		default:
			continue
		}
	}

	if hasConflicts {
		err = w.r.Storer.SetMergeMsg(fb.String())
		if err != nil {
			return false, "", err
		}

		b.WriteString("Automatic merge failed; fix conflicts and then commit the result.\n")
		mergeMsg = b.String()
	} else {
		msg := fmt.Sprintf("Merge branch '%s'\n\n", theirs) + `# Please enter a commit message to explain why this merge is necessary,
# especially if it merges an updated upstream into a topic branch.
#
# Lines starting with '#' will be ignored, and an empty message aborts
# the commit.`

		err = w.r.Storer.SetMergeMsg(msg)
		if err != nil {
			return false, "", err
		}

		mergeMsg = "Create merge commit to continue merge process"
	}

	return hasConflicts, mergeMsg, nil
}

//AbortMerge will abort the merge process and try to reconstruct the pre-merge state
func (w *Worktree) AbortMerge() error {
	err := w.removeMergeHead()
	if err != nil {
		return err
	}

	w.r.Storer.RemoveMergeMsg()

	orig, err := w.r.OrigHead()

	if err != nil {
		return err
	}

	err = w.Reset(&ResetOptions{Commit: orig.Hash(), Mode: HardReset})

	if err != nil {
		return err
	}

	w.removeOrigHead()
	w.blobs = nil

	return nil
}

//Index returns current index
func (w *Worktree) Index() (*index.Index, error) {
	return w.r.Storer.Index()
}

func (w *Worktree) computeParent(oldC, newC *object.Commit) (*mergingCommit, error) {
	parents, err := w.getCommonParents(oldC, newC)

	if err != nil {
		return nil, err
	}

	parLen := len(parents)

	if parLen == 0 {
		return nil, fmt.Errorf("No common parent. Old: %v, new: %v", oldC, newC)
	}

	p := &mergingCommit{commit: parents[0]}

	if parLen > 1 {
		p, err = w.createVirtualParent(parents)

		if err != nil {
			return nil, err
		}
	}

	return p, nil
}

func (w *Worktree) getCommonParents(oldC, newC *object.Commit) ([]*object.Commit, error) {
	prQ := make(PriorityQueue, 0)
	heap.Init(&prQ)

	heap.Push(&prQ, w.markCommit(oldC, markParent1))
	heap.Push(&prQ, w.markCommit(newC, markParent2))

	res := []*object.Commit{}

	for prQ.interesting() {

		el := heap.Pop(&prQ).(*prioritizedCommit)
		flags := el.flags & (markParent1 | markParent2 | markStale)

		if flags == (markParent1 | markParent2) {
			if el.flags&markResult == 0 {
				el.flags |= markResult
				res = append(res, el.value)
			}

			flags |= markStale
		}

		parents, err := w.getParents(el.value)

		if err != nil {
			return nil, err
		}

		for _, p := range parents {
			heap.Push(&prQ, w.markCommit(p, flags))
		}

	}

	return res, nil
}

// Conflicts in the merge base creation do not propagate to conflicts
//in the result; the conflicted base will act as the common ancestor.
func (w *Worktree) createVirtualParent(parents []*object.Commit) (*mergingCommit, error) {
	parLen := len(parents)
	if parLen < 2 {
		return nil, fmt.Errorf("createVirtualParent needs more than 2 parents. Has: %d", parLen)
	}

	recursionLevel := 1
	base := &mergingCommit{commit: parents[0]}

	for i := 1; i < parLen; i++ {
		recursionLevel++
		other := &mergingCommit{commit: parents[i]}

		newBase, _, err := w.mergeCommits(nil, base, other, recursionLevel)

		if err != nil {
			return nil, err
		}

		base = newBase
		other = nil
		newBase = nil
	}
	base.isVirtual = true

	idx, err := w.r.Storer.Index()
	if err != nil {
		return nil, err
	}
	base.idx = idx

	return base, nil
}

func (w *Worktree) mergeCommits(base, ours, theirs *mergingCommit, recursionLevel int) (*mergingCommit, map[string]*mergingResult, error) {
	if base == nil {
		cb, err := w.computeParent(ours.commit, theirs.commit)

		if err != nil {
			return nil, nil, err
		}

		base = cb
	}

	t, err := ours.commit.Tree()
	if err != nil {
		return nil, nil, err
	}

	err = w.resetIndex(t)
	if err != nil {
		return nil, nil, err
	}

	res1, err := w.getMergingDiff(base, ours)

	if err != nil {
		return nil, nil, err
	}

	res2, err := w.getMergingDiff(base, theirs)

	if err != nil {
		return nil, nil, err
	}

	changes, err := w.compareCommitsChanges(res1, res2)

	if err != nil {
		return nil, nil, err
	}

	return base, changes, nil
}

type mergingChanges struct {
	base    *mergingCommit
	commit  *mergingCommit
	changes merkletrie.Changes
}

type mergingResult struct {
	oursStatus   StatusCode
	theirsStatus StatusCode

	diffType mergeDiffType

	modifiedFile []byte
}

// Types of changes when files are merged from branch to branch
type mergeDiffType int

const (
	// No conflict - a change only occurs in one branch
	mergeDiffNoConflict mergeDiffType = iota + 1
	// Occurs when a file is modified in both branches and cannot be merged automatically because of conflicts
	mergeDiffBothModifiedWithConflicts
	// Occurs when a file is modified in both branches and can be merged automatically
	mergeDiffBothModifiedWithoutConflicts
	// Occurs when a file is added in both branches
	mergeDiffBothAdded
	// Occurs when a file is deleted in both branches
	mergeDiffBothDeleted
	// Occurs when a file is modified in first branch and deleted in second
	mergeDiffModifiedDeleted
	// Occurs when a file is modified in second branch and deleted in first
	mergeDiffDeletedModified
)

type mergingFileInfo struct {
	path                string
	unResolvedConflicts int
}

func (w *Worktree) compareCommitsChanges(ours, theirs *mergingChanges) (map[string]*mergingResult, error) {
	statuses1 := make(Status)
	oursIdx, err := w.r.Storer.Index()

	if err != nil {
		return nil, err
	}

	for _, ch := range ours.changes {
		a, err := ch.Action()
		if err != nil {
			return nil, err
		}

		switch a {
		case merkletrie.Delete:
			statuses1.File(ch.From.String()).Staging = Deleted
		case merkletrie.Insert:
			statuses1.File(ch.To.String()).Staging = Added
		case merkletrie.Modify:
			statuses1.File(ch.To.String()).Staging = Modified
		}
	}

	statuses2 := make(Status)

	for _, ch := range theirs.changes {
		a, err := ch.Action()
		if err != nil {
			return nil, err
		}

		switch a {
		case merkletrie.Delete:
			statuses2.File(ch.From.String()).Staging = Deleted
		case merkletrie.Insert:
			statuses2.File(ch.To.String()).Staging = Added
		case merkletrie.Modify:
			statuses2.File(ch.To.String()).Staging = Modified
		}
	}

	res := make(map[string]*mergingResult)

	for path, s1 := range statuses1 {
		s2, ok := statuses2[path]

		if ok {
			c := &mergingResult{oursStatus: s1.Staging, theirsStatus: s2.Staging}

			if s1.Staging == s2.Staging {
				switch s1.Staging {
				case Modified:
					{
						baseB, err := w.getBlob(ours.base, path)

						if err != nil {
							return nil, err
						}

						w.addOrUpdateBlobToCache(path, baseB, index.AncestorMode)

						oursB, err := w.getBlob(ours.commit, path)

						if err != nil {
							return nil, err
						}
						w.addOrUpdateBlobToCache(path, oursB, index.OurMode)

						theirsB, err := w.getBlob(theirs.commit, path)

						if err != nil {
							return nil, err
						}
						w.addOrUpdateBlobToCache(path, theirsB, index.TheirMode)

						mergeRes, err := w.mergeFiles(oursIdx, path, baseB, oursB, theirsB)

						if err != nil {
							return nil, err
						}

						if mergeRes.unResolvedConflicts == 0 {
							c.diffType = mergeDiffBothModifiedWithoutConflicts
						} else {
							c.diffType = mergeDiffBothModifiedWithConflicts
						}

						res[path] = c
					}
				case Added:
					{
						c.diffType = mergeDiffBothAdded
						res[path] = c

						oursB, err := w.getBlob(ours.commit, path)
						if err != nil {
							return nil, err
						}
						w.addOrUpdateBlobToCache(path, oursB, index.OurMode)

						theirsB, err := w.getBlob(theirs.commit, path)
						if err != nil {
							return nil, err
						}
						w.addOrUpdateBlobToCache(path, theirsB, index.TheirMode)

						err = w.writeBothAddedConflictFile(path, oursB, theirsB)
						if err != nil {
							return nil, err
						}

						err = w.addConflictFile(oursIdx, path, plumbing.ZeroHash, oursB.Hash, theirsB.Hash)
						if err != nil {
							return nil, err
						}
					}
				case Deleted:
					{
						c.diffType = mergeDiffNoConflict
						res[path] = c
					}
				default:
					{
						return nil, fmt.Errorf("Unexpected changes status during merging: %s", s1.Staging)
					}

				}
			} else {

				switch s1.Staging {
				case Modified:
					{
						if s2.Staging == Deleted {
							c.diffType = mergeDiffModifiedDeleted
							res[path] = c

							baseB, err := w.getBlob(ours.base, path)
							if err != nil {
								return nil, err
							}
							w.addOrUpdateBlobToCache(path, baseB, index.AncestorMode)

							oursB, err := w.getBlob(ours.commit, path)
							if err != nil {
								return nil, err
							}
							w.addOrUpdateBlobToCache(path, oursB, index.OurMode)

							err = w.addConflictFile(oursIdx, path, baseB.Hash, oursB.Hash, plumbing.ZeroHash)
							if err != nil {
								return nil, err
							}

						} else {
							c.diffType = mergeDiffNoConflict
							res[path] = c
						}
					}
				case Added:
					{
						c.diffType = mergeDiffNoConflict
						res[path] = c
					}
				case Deleted:
					{
						if s2.Staging == Modified {
							c.diffType = mergeDiffDeletedModified
							res[path] = c

							baseB, err := w.getBlob(ours.base, path)
							if err != nil {
								return nil, err
							}
							w.addOrUpdateBlobToCache(path, baseB, index.AncestorMode)

							theirsB, err := w.getBlob(theirs.commit, path)
							if err != nil {
								return nil, err
							}
							w.addOrUpdateBlobToCache(path, theirsB, index.TheirMode)

							err = w.addConflictFile(oursIdx, path, baseB.Hash, plumbing.ZeroHash, theirsB.Hash)
							if err != nil {
								return nil, err
							}

						} else {
							c.diffType = mergeDiffNoConflict
							res[path] = c
						}
					}
				default:
					{
						return nil, fmt.Errorf("Unexpected changes status during merging: %s", s1.Staging)
					}
				}
			}
		} else {
			res[path] = &mergingResult{diffType: mergeDiffNoConflict, oursStatus: s1.Staging}
		}
	}

	for path, s2 := range statuses2 {
		_, ok := statuses1[path]

		if !ok {
			switch s2.Staging {
			case Modified: //impossible case
				{
					err = w.copyFileToOurs(path, theirs.commit.commit)
					if err != nil {
						return nil, err
					}
				}
			case Added:
				{
					err = w.copyFileToOurs(path, theirs.commit.commit)
					if err != nil {
						return nil, err
					}
				}
			case Deleted:
				{
					err = w.Remove(path)
					if err != nil {
						return nil, err
					}
				}
			default:
				{
					return nil, fmt.Errorf("Unexpected changes status during merging: %s", s2.Staging)
				}
			}

			res[path] = &mergingResult{diffType: mergeDiffNoConflict, theirsStatus: s2.Staging}
		}
	}

	return res, nil
}

func (w *Worktree) writeBothAddedConflictFile(path string, ours, theirs *object.Blob) error {

	temp, err := w.Filesystem.Create(fmt.Sprintf("temp_%d", rand.Int()))

	if err != nil {
		return err
	}

	_, err = temp.Write(delimStart)
	if err != nil {
		return err
	}

	readerOurs, err := ours.Reader()
	if err != nil {
		return err
	}

	oursSc := bufio.NewScanner(readerOurs)

	for oursSc.Scan() {
		temp.Write(append(oursSc.Bytes(), '\n'))
	}

	err = oursSc.Err()
	if err != nil {
		return err
	}

	_, err = temp.Write(delimMiddle)
	if err != nil {
		return err
	}

	readerTheirs, err := theirs.Reader()
	if err != nil {
		return err
	}

	theirsSc := bufio.NewScanner(readerTheirs)

	for theirsSc.Scan() {
		temp.Write(append(theirsSc.Bytes(), '\n'))
	}

	err = theirsSc.Err()
	if err != nil {
		return err
	}

	_, err = temp.Write(delimEnd)
	if err != nil {
		return err
	}

	//rename temp file with merging result
	err = w.Filesystem.Rename(temp.Name(), path)
	if err != nil {
		return err
	}

	return nil
}

func (w *Worktree) copyFileToOurs(path string, theirsC *object.Commit) error {
	f, err := theirsC.File(path)

	if err != nil {
		return err
	}

	r, err := f.Reader()
	if err != nil {
		return err
	}

	oursF, err := w.Filesystem.Create(f.Name)
	if err != nil {
		return err
	}

	buf := make([]byte, 4*1024*1024)
	_, err = io.CopyBuffer(oursF, r, buf)

	if err != nil {
		w.Filesystem.Remove(path)

		return err
	}

	err = w.Add(path)
	if err != nil {
		w.Filesystem.Remove(path)

		return err
	}

	return nil
}

func (w *Worktree) getBlob(c *mergingCommit, path string) (*object.Blob, error) {
	if c.isVirtual {

		entries, err := c.idx.Entry(path)

		if err != nil {
			if err == index.ErrEntryNotFound {
				f, err := c.commit.File(path)

				if err != nil {
					return nil, err
				}

				return &f.Blob, nil
			}
			return nil, err
		}

		h := plumbing.ZeroHash
		for _, e := range entries {
			if e.Stage == index.Merged || e.Stage == index.OurMode {
				h = e.Hash
				break
			}
		}

		b, err := w.r.BlobObject(h)
		if err != nil {
			return nil, err
		}

		return b, nil
	}

	f, err := c.commit.File(path)

	if err != nil {
		return nil, err
	}

	return &f.Blob, nil
}

func (w *Worktree) addOrUpdateBlobToCache(path string, b *object.Blob, st index.Stage) {
	if w.blobs == nil {
		w.blobs = map[string][]*blobInfo{}
	}

	blobs, ok := w.blobs[path]
	if !ok {
		w.blobs[path] = []*blobInfo{&blobInfo{path: path, stage: st, blob: b}}
		return
	}

	for _, blobInfo := range blobs {
		if blobInfo.stage == st {

			blobInfo.blob = b

			return
		}
	}

	blobs = append(blobs, &blobInfo{path: path, stage: st, blob: b})
	w.blobs[path] = blobs

	return
}

func (w *Worktree) mergeFiles(idx *index.Index, path string, baseB, oursB, theirsB *object.Blob) (*mergingFileInfo, error) {

	diff3 := NewDiff3()
	res, err := diff3.Merge(baseB, oursB, theirsB, w.Filesystem)

	if err != nil {
		return nil, err
	}
	//rename temp file with merging result
	err = w.Filesystem.Rename(res.path, path)
	res.path = path

	if err != nil {
		return nil, err
	}

	if res.unResolvedConflicts != 0 {
		err = w.addConflictFile(idx, path, baseB.Hash, oursB.Hash, theirsB.Hash)
		if err != nil {
			return nil, err
		}
	} else {
		err = w.Add(path)
		if err != nil {
			return nil, err
		}
	}

	return res, nil
}

func (w *Worktree) getMergingDiff(base, c *mergingCommit) (*mergingChanges, error) {
	var from noder.Noder

	if base.isVirtual {
		from = mindex.NewRootNode(base.idx)
	} else {
		btree, err := base.commit.Tree()

		if err != nil {
			return nil, err
		}

		if btree != nil {
			from = object.NewTreeRootNode(btree)
		}
	}

	toTree, err := c.commit.Tree()

	if err != nil {
		return nil, err
	}

	var to noder.Noder
	if toTree != nil {
		to = object.NewTreeRootNode(toTree)

	}

	res, err := merkletrie.DiffTree(from, to, diffTreeIsEquals)

	if err != nil {
		return nil, err
	}

	return &mergingChanges{base: base, commit: c, changes: res}, nil
}

func (w *Worktree) getParents(c *object.Commit) ([]*object.Commit, error) {
	res := []*object.Commit{}

	for _, h := range c.ParentHashes {
		p, err := object.GetCommit(w.r.Storer, h)

		if err != nil {
			return nil, err
		}

		res = append(res, p)

	}

	return res, nil
}

func (w *Worktree) markCommit(c *object.Commit, flags uint32) *prioritizedCommit {
	return &prioritizedCommit{value: c, flags: flags, priority: c.Author.When}
}
