package git

import (
	"bufio"
	"bytes"
	"fmt"
	"math/rand"
	"testing"

	"gopkg.in/src-d/go-billy.v4/osfs"
)

var mergeFilesTests = []struct {
	ours      string
	theirs    string
	result    string
	conflicts int
}{
	{ //remove blocks only from ours
		ours:      "merge_remove/from_ours/ours.txt",
		theirs:    "merge_remove/from_ours/theirs.txt",
		result:    "merge_remove/from_ours/result.txt",
		conflicts: 0,
	},
	{ //remove blocks only from theirs
		ours:      "merge_remove/from_theirs/ours.txt",
		theirs:    "merge_remove/from_theirs/theirs.txt",
		result:    "merge_remove/from_theirs/result.txt",
		conflicts: 0,
	},
	{ //remove blocks from ours and theirs in different parts
		ours:      "merge_remove/from_both_1/ours.txt",
		theirs:    "merge_remove/from_both_1/theirs.txt",
		result:    "merge_remove/from_both_1/result.txt",
		conflicts: 0,
	},
	{ //remove blocks from ours and theirs at the same part
		ours:      "merge_remove/from_both_2/ours.txt",
		theirs:    "merge_remove/from_both_2/theirs.txt",
		result:    "merge_remove/from_both_2/result.txt",
		conflicts: 0,
	},
	{ //remove blocks from ours and theirs with conflict
		ours:      "merge_remove/from_both_3/ours.txt",
		theirs:    "merge_remove/from_both_3/theirs.txt",
		result:    "merge_remove/from_both_3/result.txt",
		conflicts: 1,
	},
	{ //add blocks only to ours
		ours:      "merge_add_blocks/to_ours/ours.txt",
		theirs:    "merge_add_blocks/to_ours/theirs.txt",
		result:    "merge_add_blocks/to_ours/result.txt",
		conflicts: 0,
	},
	{ //add blocks only to theirs
		ours:      "merge_add_blocks/to_theirs/ours.txt",
		theirs:    "merge_add_blocks/to_theirs/theirs.txt",
		result:    "merge_add_blocks/to_theirs/result.txt",
		conflicts: 0,
	},
	{ //add different blocks to ours and theirs without conflict
		ours:      "merge_add_blocks/to_both_1/ours.txt",
		theirs:    "merge_add_blocks/to_both_1/theirs.txt",
		result:    "merge_add_blocks/to_both_1/result.txt",
		conflicts: 0,
	},
	{ //add different blocks to ours and theirs with conflict
		ours:      "merge_add_blocks/to_both_2/ours.txt",
		theirs:    "merge_add_blocks/to_both_2/theirs.txt",
		result:    "merge_add_blocks/to_both_2/result.txt",
		conflicts: 1,
	},
	{ //add blocks to ours and remove blocks from theirs
		ours:      "merge_add_remove_blocks/add_ours_remove_theirs/ours.txt",
		theirs:    "merge_add_remove_blocks/add_ours_remove_theirs/theirs.txt",
		result:    "merge_add_remove_blocks/add_ours_remove_theirs/result.txt",
		conflicts: 0,
	},
	{ //add blocks to theirs and remove blocks from ours
		ours:      "merge_add_remove_blocks/add_theirs_remove_ours/ours.txt",
		theirs:    "merge_add_remove_blocks/add_theirs_remove_ours/theirs.txt",
		result:    "merge_add_remove_blocks/add_theirs_remove_ours/result.txt",
		conflicts: 0,
	},
	{ //add blocks to ours and theirs and remove blocks from ours and theirs without conflict
		ours:      "merge_add_remove_blocks/add_both_remove_both_1/ours.txt",
		theirs:    "merge_add_remove_blocks/add_both_remove_both_1/theirs.txt",
		result:    "merge_add_remove_blocks/add_both_remove_both_1/result.txt",
		conflicts: 0,
	},
	{ //add blocks to ours and theirs and remove blocks from ours and theirs with conflicts
		ours:      "merge_add_remove_blocks/add_both_remove_both_2/ours.txt",
		theirs:    "merge_add_remove_blocks/add_both_remove_both_2/theirs.txt",
		result:    "merge_add_remove_blocks/add_both_remove_both_2/result.txt",
		conflicts: 2,
	},
}

func TestDiff3Merge(t *testing.T) {
	fs := osfs.New("testdata/")
	diff3 := &diff3{}

	for i, tt := range mergeFilesTests {

		temp, err := fs.Create(fmt.Sprintf("temp_%d", rand.Int()))
		if err != nil {
			t.Fatal(err)
		}

		base, err := fs.Open("/merge_add_remove_blocks/base.txt")
		if err != nil {
			t.Fatal(err)
		}

		ours, err := fs.Open(tt.ours)
		if err != nil {
			t.Fatal(err)
		}

		theirs, err := fs.Open(tt.theirs)
		if err != nil {
			t.Fatal(err)
		}

		err = diff3.setupWithFiles(&base, &ours, &theirs)
		if err != nil {
			t.Fatal(err)
		}

		conf, err := diff3.writeChunks(&temp)
		if err != nil {
			t.Error(err)
		}

		fmt.Printf("Test number: %d, conflicts: %d\n", i, conf)

		temp, err = fs.Open(temp.Name())
		if err != nil {
			t.Fatal(err)
		}
		defer temp.Close()

		scTemp := bufio.NewScanner(temp)

		resFile, err := fs.Open(tt.result)
		if err != nil {
			t.Fatal(err)
		}
		defer resFile.Close()

		scResult := bufio.NewScanner(resFile)
		i := 1

		for scResult.Scan() {
			if scTemp.Scan() {
				if !bytes.Equal(scResult.Bytes(), scTemp.Bytes()) {
					t.Fatalf("Different bytes at line: %d", i)
				}
			} else {
				t.Fatalf("temp file has no lines. Result line: %d", i)
			}

			i++
		}

		for scTemp.Scan() {
			t.Errorf("Too long temp file. Temp string: %s", scTemp.Text())
		}

		if conf != tt.conflicts {
			t.Errorf("Wrong conflicts number! Must: %d, has: %d", tt.conflicts, conf)
		}

		err = fs.Remove(temp.Name())
		if err != nil {
			t.Error(err)
		}
	}
}
