package mysqlfs

import (
	"fmt"
	"testing"
	"time"

	"gopkg.in/src-d/go-git.v4"
	"gopkg.in/src-d/go-git.v4/plumbing/cache"
	"gopkg.in/src-d/go-git.v4/plumbing/object"
	"gopkg.in/src-d/go-git.v4/storage/filesystem"
)

func TestCommit(t *testing.T) {

	db, err := createDB(connStr)
	if err != nil {
		t.Error(err)
	}

	fs, err := New(db, tableName)

	if err != nil {
		t.Error(err)
	}

	fs1, err := New(db, tableName1)

	if err != nil {
		t.Error(err)
	}

	s := filesystem.NewStorage(fs, cache.NewObjectLRUDefault())
	r, err := git.Init(s, fs1)

	if err != nil {
		t.Fatal(err)
	}

	wt, err := r.Worktree()
	if err != nil {
		t.Fatal(err)
	}

	f, err := fs1.Create("README.md")
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("hello, go-git!"))

	_, err = wt.Add("README.md")
	if err != nil {
		t.Fatal(err)
	}

	commit, err := wt.Commit("add README", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Jack Jonson",
			Email: "JackJonson@gmail.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		t.Error(err)
	}

	fmt.Printf("commit: %s", commit.String())

	dropTable(connStr, tableName)
	dropTable(connStr, tableName1)
}

func TestLog(t *testing.T) {
	db, err := createDB(connStr)
	if err != nil {
		t.Error(err)
	}

	fs, err := New(db, tableName)

	if err != nil {
		t.Error(err)
	}

	fs1, err := New(db, tableName1)

	if err != nil {
		t.Error(err)
	}

	s := filesystem.NewStorage(fs, cache.NewObjectLRUDefault())
	r, err := git.Init(s, fs1)

	if err != nil {
		t.Fatal(err)
	}

	w, err := r.Worktree()
	if err != nil {
		t.Fatal(err)
	}

	f, err := fs1.Create("README.md")
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("hello, go-git!"))

	_, err = w.Add("README.md")
	if err != nil {
		t.Fatal(err)
	}

	_, err = w.Commit("add README", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Jack Jonson",
			Email: "JackJonson@gmail.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		t.Error(err)
	}

	res, err := r.Log(&git.LogOptions{All: true})

	if err != nil {
		t.Error(err)
	}

	err = res.ForEach(writeObj)

	if err != nil {
		t.Error(err)
	}
	dropTable(connStr, tableName)
	dropTable(connStr, tableName1)
}

func writeObj(c *object.Commit) error {
	fmt.Printf(c.String())

	return nil
}
