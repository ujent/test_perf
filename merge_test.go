package git

import (
	"archive/zip"
	"bufio"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/src-d/go-billy.v4/osfs"
	"gopkg.in/src-d/go-git.v4/plumbing"
	"gopkg.in/src-d/go-git.v4/plumbing/cache"
	"gopkg.in/src-d/go-git.v4/plumbing/format/index"
	"gopkg.in/src-d/go-git.v4/plumbing/object"
	"gopkg.in/src-d/go-git.v4/storage/filesystem"
)

const wtFsPath = "testdata/temp"

/*
The following tests consider this history having two root commits: V and W
V---o---M----AB----A---CD1--P---C--------S-------------------Q < master
               \         \ /            /                   /
                \         X            GQ1---G < feature   /
                 \       / \          /     /             /
W---o---N----o----B---CD2---o---D----o----GQ2------------o < dev
MergeBase
----------------------------
passed  merge-base
 M, N               Commits with unrelated history, have no merge-base
 A, B    AB         Regular merge-base between two commits
 A, A    A          The merge-commit between equal commits, is the same
 Q, N    N          The merge-commit between a commit an its ancestor, is the ancestor
 C, D    CD1, CD2   Cross merges causes more than one merge-base
 G, Q    GQ1, GQ2   Feature branches including merges, causes more than one merge-base
*/

var getCommonParentsTests = []struct {
	name    string
	in, out []plumbing.Hash
}{
	{
		name: "A, B = [AB]. Regular merge-base between two commits",
		in:   []plumbing.Hash{plumbing.NewHash("29740cfaf0c2ee4bb532dba9e80040ca738f367c"), plumbing.NewHash("2c84807970299ba98951c65fe81ebbaac01030f0")},
		out:  []plumbing.Hash{plumbing.NewHash("31a7e081a28f149ee98ffd13ba1a6d841a5f46fd")},
	},
	{
		name: "C D = [CD1, CD2]. Cross merges causes more than one merge-base",
		in:   []plumbing.Hash{plumbing.NewHash("8b72fabdc4222c3ff965bc310ded788c601c50ed"), plumbing.NewHash("14777cf3e209334592fbfd0b878f6868394db836")},
		out:  []plumbing.Hash{plumbing.NewHash("4709e13a3cbb300c2b8a917effda776e1b8955c7"), plumbing.NewHash("38468e274e91e50ffb637b88a1954ab6193fe974")},
	},
	{
		name: "G Q = [GQ1, GQ2]. Feature branches including merges, causes more than one merge-base",
		in:   []plumbing.Hash{plumbing.NewHash("d1b0093698e398d596ef94d646c4db37e8d1e970"), plumbing.NewHash("dce0e0c20d701c3d260146e443d6b3b079505191")},
		out:  []plumbing.Hash{plumbing.NewHash("806824d4778e94fe7c3244e92a9cd07090c9ab54"), plumbing.NewHash("ccaaa99c21dad7e9f392c36ae8cb72dc63bed458")},
	},
	{
		name: "A A = [A]. The merge-commit between equal commits, is the same",
		in:   []plumbing.Hash{plumbing.NewHash("29740cfaf0c2ee4bb532dba9e80040ca738f367c"), plumbing.NewHash("29740cfaf0c2ee4bb532dba9e80040ca738f367c")},
		out:  []plumbing.Hash{plumbing.NewHash("29740cfaf0c2ee4bb532dba9e80040ca738f367c")},
	},
	{
		name: "Q N = [Q, N]. The merge-commit between a commit an its ancestor, is the ancestor",
		in:   []plumbing.Hash{plumbing.NewHash("dce0e0c20d701c3d260146e443d6b3b079505191"), plumbing.NewHash("d64b894762ab5f09e2b155221b90c18bd0637236")},
		out:  []plumbing.Hash{plumbing.NewHash("d64b894762ab5f09e2b155221b90c18bd0637236")},
	},
}

var lines1 = []fileLine{
	fileLine{number: 1, text: "a"},
	fileLine{number: 2, text: "b"},
	fileLine{number: 3, text: "c"},
	fileLine{number: 4, text: "a"},
	fileLine{number: 5, text: "b"},
	fileLine{number: 6, text: "b"},
	fileLine{number: 7, text: "a"},
}

var lines2 = []fileLine{
	fileLine{number: 1, text: "c"},
	fileLine{number: 2, text: "b"},
	fileLine{number: 3, text: "a"},
	fileLine{number: 4, text: "b"},
	fileLine{number: 5, text: "a"},
	fileLine{number: 6, text: "c"},
}

var matches = map[int]int{3: 1, 4: 3, 5: 4, 7: 5}

func TestGetMatches(t *testing.T) {

	md := NewMyersDifferer(lines1, lines2)
	diffRes := md.Diff()

	d := diff3{}

	matchesRes := d.getMatches(diffRes)

	for _, d := range diffRes {
		if d.diffType != fileDiffEql {
			continue
		}

		oldNum := -1
		if d.lineA != nil {
			oldNum = d.lineA.number
		}

		newNum := -1
		if d.lineB != nil {
			newNum = d.lineB.number
		}
		fmt.Printf("type: %d, oldNum: %d, newNum: %d \n", d.diffType, oldNum, newNum)
	}

	fmt.Printf("matches: %v\n", matchesRes)

	for key, val := range matches {
		m, ok := matchesRes[key]

		if !ok {
			t.Errorf("There is no match - key: %d, val: %d\n", key, val)
		}

		if m != val {
			t.Errorf("Wrong match value. Must - key: %d, val: %d, has - key: %d, val: %d\n", key, val, key, m)
		}
	}

	mustL := len(matches)
	hasL := len(matchesRes)

	if mustL != hasL {
		t.Errorf("Wrong match length. Must: %d, has: %d\n", mustL, hasL)
	}
}

func TestGetShortestPath(t *testing.T) {

	md := NewMyersDifferer(lines1, lines2)
	trace := md.GetShortestPath()
	depth := len(trace)

	fmt.Printf("path depth: %d\n", depth)

	must := 6

	if depth != must {
		t.Errorf("Wrong path length! Must: %d, has: %d", must, depth)
	}
}

func TestBacktrack(t *testing.T) {
	md := NewMyersDifferer(lines1, lines2)
	trace := md.GetShortestPath()
	fmt.Printf("trace: %v", trace)
	ch := md.Backtrack(trace)
	expected := []backtrackEl{
		backtrackEl{prevX: 7, prevY: 5, x: 7, y: 6},
		backtrackEl{prevX: 6, prevY: 4, x: 7, y: 5},
		backtrackEl{prevX: 5, prevY: 4, x: 6, y: 4},
		backtrackEl{prevX: 4, prevY: 3, x: 5, y: 4},
		backtrackEl{prevX: 3, prevY: 2, x: 4, y: 3},
		backtrackEl{prevX: 3, prevY: 1, x: 3, y: 2},
		backtrackEl{prevX: 2, prevY: 0, x: 3, y: 1},
		backtrackEl{prevX: 1, prevY: 0, x: 2, y: 0},
		backtrackEl{prevX: 0, prevY: 0, x: 1, y: 0},
	}

	i := 0

	for res := range ch {

		//fmt.Printf("index: %d	(%d, %d) -> (%d, %d)\n", i, res.prevX, res.prevY, res.x, res.y)
		ex := expected[i]

		if ex.x != res.x || ex.y != res.y || ex.prevX != res.prevX || ex.prevY != res.prevY {
			t.Errorf("Wrong backtrack element, i: %d! Must: %v; has: %v\n", i, ex, res)
		}

		i++
	}
}

func TestDiff(t *testing.T) {
	md := NewMyersDifferer(lines1, lines2)
	res := md.Diff()

	fmt.Printf("%v", res)
}

func TestGetCommonParents(t *testing.T) {
	wtPath := "testdata/dotgittest/dotgit"
	path := "testdata/dotgit.zip"
	unzipPath := "testdata/dotgittest"

	removeFolder(unzipPath)
	removeTempFolder()

	_, err := unzip(path, unzipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer removeFolder(unzipPath)
	defer removeTempFolder()

	wt, err := getWorktree(wtPath)

	if err != nil {
		t.Fatal(err)
	}

	for _, tt := range getCommonParentsTests {
		s := wt.r.Storer
		old := tt.in[0]
		new := tt.in[1]

		oldC, err := object.GetCommit(s, old)

		if err != nil {
			t.Fatal(err)
		}

		newC, err := object.GetCommit(s, new)
		if err != nil {
			t.Fatal(err)
		}

		bases, err := wt.getCommonParents(oldC, newC)

		if err != nil {
			t.Fatal(err)
		}

		if len(bases) != len(tt.out) {
			t.Errorf("result length mismatch. Must: %d, has: %d", len(tt.out), len(bases))
		}

		for i, base := range bases {
			if base.Hash != tt.out[i] {
				t.Errorf("test number: %d. Expect: %v recieve: %v", i, tt.out[i], base.Hash)
			}
		}

		e := removeTempFolder()
		if e != nil {
			t.Error(e)
		}
	}

}

const mergeWithConfPath = "testdata/merge_func/with_conflicts"
const mergeWithoutConfPath = "testdata/merge_func/without_conflicts"
const mergeWithVirtParentPath = "testdata/merge_func/with_virt_parent"
const mergeHasUncommittedStagedPath = "testdata/merge_func/has_uncommitted/staged"
const theirsAddFilePath = "testdata/merge_func/theirs_add_file"
const ffMergePath = "testdata/merge_func/ff"

var nonFFMergeTests = []struct {
	archieveGit  string
	gitPath      string
	unzipPath    string
	branch       string
	hasConflicts bool
}{
	{
		archieveGit:  path.Join(mergeWithConfPath, "dotgit.zip"),
		gitPath:      path.Join(mergeWithConfPath, "dotgittest/dotgit"),
		unzipPath:    path.Join(mergeWithConfPath, "dotgittest"),
		hasConflicts: true,
		branch:       "topic3",
	},
	{
		archieveGit:  path.Join(mergeWithoutConfPath, "dotgit.zip"),
		gitPath:      path.Join(mergeWithoutConfPath, "dotgittest/dotgit"),
		unzipPath:    path.Join(mergeWithoutConfPath, "dotgittest"),
		hasConflicts: false,
		branch:       "topic",
	},
	{
		archieveGit:  path.Join(theirsAddFilePath, "dotgit.zip"),
		gitPath:      path.Join(theirsAddFilePath, "dotgittest/dotgit"),
		unzipPath:    path.Join(theirsAddFilePath, "dotgittest"),
		hasConflicts: false,
		branch:       "topic",
	},
}

func TestNonFastForwardMerge(t *testing.T) {
	for i, tt := range nonFFMergeTests {

		removeFolder(tt.unzipPath)
		removeTempFolder()

		_, err := unzip(tt.archieveGit, tt.unzipPath)
		if err != nil {
			t.Fatal(err)
		}

		defer removeFolder(tt.unzipPath)
		defer removeTempFolder()

		wt, err := getWorktree(tt.gitPath)

		if err != nil {
			t.Fatal(err)
		}

		head, err := wt.r.Head()
		if err != nil && err != plumbing.ErrReferenceNotFound {
			t.Fatal(err)
		}

		headHash := head.Hash()
		fmt.Printf("head hash: %s\n", headHash)

		refName := plumbing.NewBranchReferenceName(tt.branch)
		ref, err := wt.r.Storer.Reference(refName)
		if err != nil {
			t.Fatal(err)
		}
		newHash := ref.Hash()

		fmt.Printf("master: %s, topic: %s\n", headHash, newHash)
		idx, err := wt.r.Storer.Index()
		if err != nil {
			fmt.Println(err)
		}

		fmt.Println("Index before merge")

		for i, entr := range idx.Entries {
			fmt.Printf("i: %d, entry name: %s stage: %d, size: %d, hash: %v\n", i, entr.Name, entr.Stage, entr.Size, entr.Hash)
		}

		_, err = wt.nonFastForwardMerge(headHash, newHash, refName)

		if err != nil {

			if err == ErrMergeCommitNeeded {

				if tt.hasConflicts {
					t.Errorf("Test %d. Must: has conflicts; has: has no conflicts\n", i)
				}
			} else if err == ErrMergeWithConflicts {
				if !tt.hasConflicts {
					t.Errorf("Test %d. Must: has no conflicts; has: has conflicts\n", i)
				}

			} else {
				t.Error(err)
			}
		}
		idx, err = wt.r.Storer.Index()

		if err != nil {
			t.Error(err)
		} else {
			fmt.Println("Index after merge")
			for j, entr := range idx.Entries {
				fmt.Printf("j: %d, entry name: %s stage: %d, hash: %v\n", j, entr.Name, entr.Stage, entr.Hash)
			}
		}

		err = wt.AbortMerge()
		if err != nil {
			e := removeTempFolder()
			if e != nil {
				t.Error(e)
			}

			t.Fatal(err)
		}

		e := removeTempFolder()
		if e != nil {
			t.Error(e)
		}
	}
}

var abortMergeTests = []struct {
	archieveGit  string
	gitPath      string
	unzipPath    string
	branch       string
	hasConflicts bool
}{
	{
		archieveGit:  path.Join(mergeWithConfPath, "dotgit.zip"),
		gitPath:      path.Join(mergeWithConfPath, "dotgittest/dotgit"),
		unzipPath:    path.Join(mergeWithConfPath, "dotgittest"),
		branch:       "topic3",
		hasConflicts: true,
	},
	{
		archieveGit:  path.Join(mergeWithoutConfPath, "dotgit.zip"),
		gitPath:      path.Join(mergeWithoutConfPath, "dotgittest/dotgit"),
		unzipPath:    path.Join(mergeWithoutConfPath, "dotgittest"),
		branch:       "topic",
		hasConflicts: false,
	},
}

func TestAbortMerge(t *testing.T) {
	for i, tt := range abortMergeTests {

		removeFolder(tt.unzipPath)
		removeTempFolder()

		_, err := unzip(tt.archieveGit, tt.unzipPath)
		if err != nil {
			t.Fatal(err)
		}

		defer removeFolder(tt.unzipPath)
		defer removeTempFolder()

		wt, err := getWorktree(tt.gitPath)

		if err != nil {
			t.Fatal(err)
		}

		head, err := wt.r.Head()
		if err != nil && err != plumbing.ErrReferenceNotFound {
			t.Fatal(err)
		}

		headHash := head.Hash()

		refName := plumbing.NewBranchReferenceName(tt.branch)
		ref, err := wt.r.Storer.Reference(refName)
		if err != nil {
			t.Fatal(err)
		}
		newHash := ref.Hash()

		idx1, err := wt.r.Storer.Index()
		if err != nil {
			fmt.Println(err)
		}

		fmt.Println("Index before merge")

		for i, entr := range idx1.Entries {
			fmt.Printf("i: %d, entry name: %s stage: %d, size: %d, hash: %v\n", i, entr.Name, entr.Stage, entr.Size, entr.Hash)
		}

		_, err = wt.nonFastForwardMerge(headHash, newHash, refName)

		if err != nil {

			if err == ErrMergeCommitNeeded {

				if tt.hasConflicts {
					t.Errorf("Test %d. Must: has conflicts; has: has no conflicts\n", i)
				}
			} else if err == ErrMergeWithConflicts {
				if !tt.hasConflicts {
					t.Errorf("Test %d. Must: has no conflicts; has: has conflicts\n", i)
				}

			} else {
				t.Error(err)
			}
		}
		idx2, err := wt.r.Storer.Index()

		if err != nil {
			t.Error(err)
		} else {
			fmt.Println("Index after merge")
			for j, entr := range idx2.Entries {
				fmt.Printf("j: %d, entry name: %s stage: %d, hash: %v\n", j, entr.Name, entr.Stage, entr.Hash)
			}
		}

		err = wt.AbortMerge()
		if err != nil {
			e := removeTempFolder()
			if e != nil {
				t.Error(e)
			}

			t.Fatal(err)
		}

		idx3, err := wt.r.Storer.Index()

		if len(idx1.Entries) != len(idx3.Entries) {
			t.Errorf("Wrong length of index entries. Must: %d, has: %d\n", len(idx1.Entries), len(idx3.Entries))
		}

		for _, e1 := range idx1.Entries {
			isFound := false

			for _, e3 := range idx3.Entries {

				if e1.Name == e3.Name && e1.Hash == e3.Hash && e1.Stage == e3.Stage {
					isFound = true
					break
				}
			}

			if !isFound {
				t.Errorf("First state entry isn't found. Name: %s, hash: %v, stage: %d", e1.Name, e1.Hash, e1.Stage)
			}
		}

		for _, e3 := range idx3.Entries {
			isFound := false

			for _, e1 := range idx1.Entries {

				if e1.Name == e3.Name && e1.Hash == e3.Hash && e1.Stage == e3.Stage {
					isFound = true
					break
				}
			}

			if !isFound {
				t.Errorf("Last state entry isn't found. Name: %s, hash: %v, stage: %d", e3.Name, e3.Hash, e3.Stage)
			}
		}

		msg, err := wt.MergeMsg()
		if err != nil {
			t.Error(err)
		}

		if msg != "" {
			t.Error("Merge msg wasn't deleted")
		}

		mh, err := wt.r.MergeHead()
		if err != nil {
			t.Error(err)
		}

		if mh != nil {
			t.Error("MERGE_HEAD wasn't deleted")
		}

		orig, err := wt.r.OrigHead()
		if err != nil && err != plumbing.ErrReferenceNotFound {
			t.Error(err)
		}

		if orig != nil {
			t.Error("ORIG_HEAD wasn't deleted")
		}

		err = removeTempFolder()
		if err != nil {
			t.Error(err)
		}
	}
}

var mergeTests = []struct {
	archieveGit    string
	gitPath        string
	unzipPath      string
	branch         string
	hasConflicts   bool
	isFF           bool
	hasUncommitted bool
}{
	{
		archieveGit:    path.Join(mergeWithConfPath, "dotgit.zip"),
		gitPath:        path.Join(mergeWithConfPath, "dotgittest/dotgit"),
		unzipPath:      path.Join(mergeWithConfPath, "dotgittest"),
		branch:         "topic3",
		hasConflicts:   true,
		isFF:           false,
		hasUncommitted: false,
	},
	{
		archieveGit:    path.Join(mergeWithoutConfPath, "dotgit.zip"),
		gitPath:        path.Join(mergeWithoutConfPath, "dotgittest/dotgit"),
		unzipPath:      path.Join(mergeWithoutConfPath, "dotgittest"),
		branch:         "topic",
		hasConflicts:   false,
		isFF:           false,
		hasUncommitted: false,
	},
	{
		archieveGit:    path.Join(ffMergePath, "dotgit.zip"),
		gitPath:        path.Join(ffMergePath, "dotgittest/dotgit"),
		unzipPath:      path.Join(ffMergePath, "dotgittest"),
		branch:         "topic1",
		hasConflicts:   false,
		isFF:           true,
		hasUncommitted: false,
	},
	{
		archieveGit:    path.Join(mergeWithVirtParentPath, "dotgit.zip"),
		gitPath:        path.Join(mergeWithVirtParentPath, "dotgittest/dotgit"),
		unzipPath:      path.Join(mergeWithVirtParentPath, "dotgittest"),
		branch:         "topic",
		hasConflicts:   false,
		isFF:           false,
		hasUncommitted: false,
	},
	{
		archieveGit:    path.Join(mergeHasUncommittedStagedPath, "dotgit.zip"),
		gitPath:        path.Join(mergeHasUncommittedStagedPath, "dotgittest/dotgit"),
		unzipPath:      path.Join(mergeHasUncommittedStagedPath, "dotgittest"),
		branch:         "topic",
		hasConflicts:   false,
		isFF:           false,
		hasUncommitted: true,
	},
}

func TestMerge(t *testing.T) {
	for i, tt := range mergeTests {
		removeFolder(tt.unzipPath)
		removeTempFolder()

		_, err := unzip(tt.archieveGit, tt.unzipPath)
		if err != nil {
			t.Fatal(err)
		}

		defer removeFolder(tt.unzipPath)
		defer removeTempFolder()

		wt, err := getWorktree(tt.gitPath)

		if err != nil {
			t.Fatal(err)
		}

		idx, err := wt.r.Storer.Index()
		if err != nil {
			fmt.Println(err)
		}

		fmt.Println("Index before merge")

		for i, entr := range idx.Entries {
			fmt.Printf("i: %d, entry name: %s stage: %d, size: %d, hash: %v\n", i, entr.Name, entr.Stage, entr.Size, entr.Hash)
		}

		_, err = wt.Merge(tt.branch)

		if err != nil {

			switch err {
			case ErrMergeCommitNeeded:
				{
					if tt.hasUncommitted {
						t.Fatalf("Test: %d. Has no uncommitted changes,but must have", i)
					}

					if tt.isFF {

						t.Fatalf("Test: %d. Must: ff, has: ErrMergeCommitNeeded", i)
					}
				}
			case ErrMergeWithConflicts:
				{
					if tt.hasUncommitted {
						t.Fatalf("Test: %d. Has no uncommitted changes,but must have", i)
					}

					if tt.isFF {

						t.Fatalf("Test: %d. Must: ff, has: ErrMergeWithConflicts", i)
					}
				}
			case ErrHasUncommittedFiles:
				{
					if !tt.hasUncommitted {
						t.Fatalf("Test: %d. Has uncommitted changes,but must not", i)
					}
				}
			default:
				{
					t.Error(err)
				}
			}

			if tt.hasUncommitted {
				continue
			}

			idx, err = wt.r.Storer.Index()

			fmt.Println(tt.unzipPath)

			if err != nil {
				t.Error(err)
			} else {
				fmt.Println("Index after merge")
				for j, entr := range idx.Entries {
					fmt.Printf("j: %d, entry name: %s stage: %d, hash: %v\n", j, entr.Name, entr.Stage, entr.Hash)
				}
			}
		} else {

			if tt.hasUncommitted {
				t.Fatalf("Test: %d. Has no uncommitted changes,but must have", i)
			}
		}
	}
}

//test with conflicts
func TestLogMergeConflicts1(t *testing.T) {

	archieveGit := path.Join(mergeWithConfPath, "dotgit.zip")
	gitPath := path.Join(mergeWithConfPath, "dotgittest/dotgit")
	unzipPath := path.Join(mergeWithConfPath, "dotgittest")

	removeFolder(unzipPath)

	_, err := unzip(archieveGit, unzipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer removeFolder(unzipPath)

	wt, err := getWorktree(gitPath)

	if err != nil {
		t.Fatal(err)
	}

	conf := map[string]*mergingResult{
		"files/file1": &mergingResult{diffType: mergeDiffNoConflict},
		"files/file2": &mergingResult{diffType: mergeDiffBothModifiedWithConflicts},
		"files/file3": &mergingResult{diffType: mergeDiffBothModifiedWithoutConflicts},
		"files/file4": &mergingResult{diffType: mergeDiffBothAdded},
		"files/file5": &mergingResult{diffType: mergeDiffBothDeleted},
		"files/file6": &mergingResult{diffType: mergeDiffModifiedDeleted},
		"files/file7": &mergingResult{diffType: mergeDiffDeletedModified},
	}

	hasConf, _, err := wt.logMergeConflicts(conf, plumbing.NewBranchReferenceName("topic"))

	if err != nil {
		t.Fatal(err)
	}

	if !hasConf {
		t.Fatal("Has no conflicts, but must have")
	}

	mergeFileContent, err := wt.r.Storer.MergeMsgFileContent()
	if err != nil {
		t.Fatal(err)
	}

	if mergeFileContent == "" {
		t.Fatal("MERGE_FILE is empty")
	}

	fmt.Println(mergeFileContent)

	wt.r.Storer.RemoveMergeMsg()
}

//test without conflicts
func TestLogMergeConflicts2(t *testing.T) {

	archieveGit := path.Join(mergeWithoutConfPath, "dotgit.zip")
	gitPath := path.Join(mergeWithoutConfPath, "dotgittest/dotgit")
	unzipPath := path.Join(mergeWithoutConfPath, "dotgittest")

	removeFolder(unzipPath)

	_, err := unzip(archieveGit, unzipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer removeFolder(unzipPath)

	wt, err := getWorktree(gitPath)

	if err != nil {
		t.Fatal(err)
	}

	conf := map[string]*mergingResult{
		"files/file1": &mergingResult{diffType: mergeDiffNoConflict},
		"files/file2": &mergingResult{diffType: mergeDiffBothModifiedWithoutConflicts},
		"files/file3": &mergingResult{diffType: mergeDiffBothDeleted},
	}

	hasConf, _, err := wt.logMergeConflicts(conf, plumbing.NewBranchReferenceName("topic"))

	if err != nil {
		t.Fatal(err)
	}

	if hasConf {
		t.Fatal("Has conflicts, but must not have")
	}

	mergeFileContent, err := wt.r.Storer.MergeMsgFileContent()
	if err != nil {
		t.Fatal(err)
	}

	if mergeFileContent == "" {
		t.Fatal("MERGE_FILE is empty")
	}

	fmt.Println(mergeFileContent)

	wt.r.Storer.RemoveMergeMsg()
}

//with conflicts
func TestMergeMsg1(t *testing.T) {

	archieveGit := path.Join(mergeWithConfPath, "dotgit.zip")
	gitPath := path.Join(mergeWithConfPath, "dotgittest/dotgit")
	unzipPath := path.Join(mergeWithConfPath, "dotgittest")

	removeFolder(unzipPath)

	_, err := unzip(archieveGit, unzipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer removeFolder(unzipPath)

	wt, err := getWorktree(gitPath)

	if err != nil {
		t.Fatal(err)
	}

	conf := map[string]*mergingResult{
		"files/file1": &mergingResult{diffType: mergeDiffNoConflict},
		"files/file2": &mergingResult{diffType: mergeDiffBothModifiedWithConflicts},
		"files/file3": &mergingResult{diffType: mergeDiffBothModifiedWithoutConflicts},
		"files/file4": &mergingResult{diffType: mergeDiffBothAdded},
		"files/file5": &mergingResult{diffType: mergeDiffBothDeleted},
		"files/file6": &mergingResult{diffType: mergeDiffModifiedDeleted},
		"files/file7": &mergingResult{diffType: mergeDiffDeletedModified},
	}

	_, _, err = wt.logMergeConflicts(conf, plumbing.NewBranchReferenceName("topic"))

	if err != nil {
		t.Fatal(err)
	}

	msg, err := wt.MergeMsg()
	if err != nil {
		t.Fatal(err)
	}

	fmt.Println(msg)

	fullMsg, err := wt.MergeMsgFileContent()
	if err != nil {
		t.Fatal(err)
	}

	fmt.Println(fullMsg)
}

//without conflicts
func TestMergeMsg2(t *testing.T) {

	archieveGit := path.Join(mergeWithoutConfPath, "dotgit.zip")
	gitPath := path.Join(mergeWithoutConfPath, "dotgittest/dotgit")
	unzipPath := path.Join(mergeWithoutConfPath, "dotgittest")

	removeFolder(unzipPath)

	_, err := unzip(archieveGit, unzipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer removeFolder(unzipPath)

	wt, err := getWorktree(gitPath)

	if err != nil {
		t.Fatal(err)
	}

	conf := map[string]*mergingResult{
		"files/file1": &mergingResult{diffType: mergeDiffNoConflict},
		"files/file2": &mergingResult{diffType: mergeDiffBothModifiedWithoutConflicts},
		"files/file3": &mergingResult{diffType: mergeDiffBothDeleted},
	}

	_, _, err = wt.logMergeConflicts(conf, plumbing.NewBranchReferenceName("topic"))

	if err != nil {
		t.Fatal(err)
	}

	msg, err := wt.MergeMsg()
	if err != nil {
		t.Fatal(err)
	}

	fmt.Print(msg)

	fullMsg, err := wt.MergeMsgFileContent()
	if err != nil {
		t.Fatal(err)
	}

	fmt.Println(fullMsg)
}

func TestAddOrUpdateBlobToCache1(t *testing.T) {
	archieveGit := path.Join(mergeWithoutConfPath, "dotgit.zip")
	gitPath := path.Join(mergeWithoutConfPath, "dotgittest/dotgit")
	unzipPath := path.Join(mergeWithoutConfPath, "dotgittest")

	removeFolder(unzipPath)
	removeTempFolder()

	_, err := unzip(archieveGit, unzipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer removeFolder(unzipPath)
	defer removeTempFolder()

	wt, err := getWorktree(gitPath)

	if err != nil {
		t.Fatal(err)
	}

	idx, err := wt.Index()
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range idx.Entries {

		b, err := wt.r.BlobObject(e.Hash)
		if err != nil {
			t.Error(err)
		}

		wt.addOrUpdateBlobToCache(e.Name, b, e.Stage)
	}

	if len(wt.blobs) == 0 {
		t.Errorf("Wrong number of cache entries!\n")
	}

	fmt.Printf("Blobs cache length: %d, cache: %v\n", len(wt.blobs), wt.blobs)
}

func TestAddOrUpdateBlobToCache2(t *testing.T) {
	archieveGit := path.Join(mergeWithoutConfPath, "dotgit.zip")
	gitPath := path.Join(mergeWithoutConfPath, "dotgittest/dotgit")
	unzipPath := path.Join(mergeWithoutConfPath, "dotgittest")

	removeFolder(unzipPath)
	removeTempFolder()

	_, err := unzip(archieveGit, unzipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer removeFolder(unzipPath)
	defer removeTempFolder()

	wt, err := getWorktree(gitPath)

	if err != nil {
		t.Fatal(err)
	}

	idx, err := wt.Index()
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range idx.Entries {

		b, err := wt.r.BlobObject(e.Hash)
		if err != nil {
			t.Error(err)
		}

		wt.addOrUpdateBlobToCache(e.Name, b, e.Stage)
		wt.addOrUpdateBlobToCache(e.Name, b, e.Stage)
	}

	if len(wt.blobs) == 0 {
		t.Errorf("Wrong number of cache entries!\n")
	}

	for path, blobs := range wt.blobs {
		must := 1
		if len(blobs) != must {
			t.Errorf("Wrong number of blobs! Path: %s, must: %d, has: %d\n", path, must, len(blobs))
		}
	}

	fmt.Printf("Blobs cache length: %d, cache: %v\n", len(wt.blobs), wt.blobs)
}

func TestAddOrUpdateBlobToCache3(t *testing.T) {
	archieveGit := path.Join(mergeWithoutConfPath, "dotgit.zip")
	gitPath := path.Join(mergeWithoutConfPath, "dotgittest/dotgit")
	unzipPath := path.Join(mergeWithoutConfPath, "dotgittest")

	removeFolder(unzipPath)
	removeTempFolder()

	_, err := unzip(archieveGit, unzipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer removeFolder(unzipPath)
	defer removeTempFolder()

	wt, err := getWorktree(gitPath)

	if err != nil {
		t.Fatal(err)
	}

	idx, err := wt.Index()
	if err != nil {
		t.Fatal(err)
	}

	var specialPath string
	for i, e := range idx.Entries {

		b, err := wt.r.BlobObject(e.Hash)
		if err != nil {
			t.Error(err)
		}

		wt.addOrUpdateBlobToCache(e.Name, b, e.Stage)

		if i == 0 {
			specialPath = e.Name
			wt.addOrUpdateBlobToCache(e.Name, nil, e.Stage)
		}
	}

	if len(wt.blobs) == 0 {
		t.Errorf("Wrong number of cache entries!\n")
	}

	sp, ok := wt.blobs[specialPath]
	if !ok {
		t.Errorf("No blob: %s\n", specialPath)
	}

	must := 1
	if len(sp) != must {
		t.Fatalf("Wrong number of blobs! Path: %s, must: %d, has: %d\n", specialPath, must, len(sp))
	}

	info := sp[0]
	if info.blob != nil {
		t.Error("Blob isn't nil!\n")
	}

	fmt.Printf("Blobs cache length: %d, cache: %v\n", len(wt.blobs), wt.blobs)
}

func TestAddOrUpdateBlobToCache4(t *testing.T) {
	archieveGit := path.Join(mergeWithoutConfPath, "dotgit.zip")
	gitPath := path.Join(mergeWithoutConfPath, "dotgittest/dotgit")
	unzipPath := path.Join(mergeWithoutConfPath, "dotgittest")

	removeFolder(unzipPath)
	removeTempFolder()

	_, err := unzip(archieveGit, unzipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer removeFolder(unzipPath)
	defer removeTempFolder()

	wt, err := getWorktree(gitPath)

	if err != nil {
		t.Fatal(err)
	}

	idx, err := wt.Index()
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range idx.Entries {

		b, err := wt.r.BlobObject(e.Hash)
		if err != nil {
			t.Error(err)
		}

		wt.addOrUpdateBlobToCache(e.Name, b, e.Stage)
		wt.addOrUpdateBlobToCache(e.Name, b, index.TheirMode)
	}

	if len(wt.blobs) == 0 {
		t.Errorf("Wrong number of cache entries!\n")
	}

	for path, blobs := range wt.blobs {
		must := 2
		if len(blobs) != must {
			t.Errorf("Wrong number of blobs! Path: %s, must: %d, has: %d\n", path, must, len(blobs))
		}
	}

	fmt.Printf("Blobs cache length: %d, cache: %v\n", len(wt.blobs), wt.blobs)
}

func TestReadFileByStage(t *testing.T) {
	archieveGit := path.Join(mergeWithConfPath, "dotgit.zip")
	gitPath := path.Join(mergeWithConfPath, "dotgittest/dotgit")
	unzipPath := path.Join(mergeWithConfPath, "dotgittest")

	removeFolder(unzipPath)
	removeTempFolder()

	_, err := unzip(archieveGit, unzipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer removeFolder(unzipPath)
	defer removeTempFolder()

	wt, err := getWorktree(gitPath)

	if err != nil {
		t.Fatal(err)
	}

	_, err = wt.Merge("topic3")
	if err != nil {

	}

	fmt.Printf("Blobs cache length: %d, cache: %v\n", len(wt.blobs), wt.blobs)

	for path, blobs := range wt.blobs {

		for _, b := range blobs {
			r, err := wt.ReadFileByStage(path, b.stage)
			if err != nil {
				t.Error(err)
			}

			fmt.Printf("Path: %s, stage: %d, file:\n %s\n", path, b.stage, r)
		}
	}

	n := "common.txt"
	f, err := wt.Filesystem.OpenFile(n, os.O_RDWR, 0666)

	if err != nil {
		t.Errorf("Reading conflict file %s error: %s", n, err)
	} else {
		fmt.Printf("Reading conflict file %s:\n", n)

		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fmt.Println(sc.Text())
		}

		if sc.Err() != nil {
			t.Error(sc.Err())
		}
	}
}

func TestConflictEntries(t *testing.T) {
	archieveGit := path.Join(mergeWithConfPath, "dotgit.zip")
	gitPath := path.Join(mergeWithConfPath, "dotgittest/dotgit")
	unzipPath := path.Join(mergeWithConfPath, "dotgittest")

	removeFolder(unzipPath)
	removeTempFolder()

	_, err := unzip(archieveGit, unzipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer removeFolder(unzipPath)
	defer removeTempFolder()

	wt, err := getWorktree(gitPath)

	if err != nil {
		t.Fatal(err)
	}

	_, err = wt.Merge("topic3")
	if err != nil {

	}

	fmt.Printf("Blobs cache length: %d, cache: %v\n", len(wt.blobs), wt.blobs)

	conf, err := wt.ConflictEntries()
	must := 1

	if len(conf) != must {
		t.Fatalf("Wrong number of conflicts! Must: %d, has: %d\n", must, len(conf))
	}

	for path, c := range conf {
		fmt.Printf("Number of entries: %d\n", len(c))

		if len(c) <= 1 {
			t.Errorf("Wrong number of entries: %d, path: %s\n", len(c), path)
		}
	}
}

func getWorktree(gitPath string) (*Worktree, error) {

	dotgit := osfs.New(gitPath)
	wtfs := osfs.New(wtFsPath)
	storage := filesystem.NewStorage(dotgit, cache.NewObjectLRUDefault())

	r, err := Open(storage, wtfs)

	if err != nil {
		return nil, err
	}

	wt, err := r.Worktree()

	if err != nil {
		return nil, err
	}

	return wt, nil
}

func removeTempFolder() error {
	err := os.RemoveAll(wtFsPath)

	if err != nil {
		return err
	}

	return nil
}

func removeFolder(path string) error {
	err := os.RemoveAll(path)

	if err != nil {
		return err
	}

	return nil
}

func TestUnzip(t *testing.T) {
	path := "testdata/dotgit.zip"
	unzipPath := "testdata/dotgittest"

	defer removeFolder(unzipPath)

	res, err := unzip(path, unzipPath)
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range res {
		fmt.Printf("File: %s\n", f)
	}

}

// Unzip will decompress a zip archive, moving all files and folders
// within the zip file (src) to an output directory (dest).
func unzip(src string, dest string) ([]string, error) {

	var filenames []string

	r, err := zip.OpenReader(src)
	if err != nil {
		return filenames, err
	}

	defer r.Close()

	for _, f := range r.File {

		// Store filename/path for returning and using later on
		fpath := filepath.Join(dest, f.Name)

		// Check for ZipSlip. More Info: http://bit.ly/2MsjAWE
		if !strings.HasPrefix(fpath, filepath.Clean(dest)+string(os.PathSeparator)) {
			return filenames, fmt.Errorf("%s: illegal file path", fpath)
		}

		filenames = append(filenames, fpath)

		if f.FileInfo().IsDir() {
			// Make Folder
			os.MkdirAll(fpath, os.ModePerm)
			continue
		}

		// Make File
		if err = os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
			return filenames, err
		}

		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return filenames, err
		}

		rc, err := f.Open()
		if err != nil {
			return filenames, err
		}

		_, err = io.Copy(outFile, rc)

		// Close the file without defer to close before next iteration of loop
		e := outFile.Close()
		if e != nil {
			fmt.Println(err)
		}

		e = rc.Close()
		if e != nil {
			fmt.Println(err)
		}

		if err != nil {
			return filenames, err
		}
	}

	return filenames, nil
}
