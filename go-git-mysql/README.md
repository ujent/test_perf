# go-git-mysql

To start go-git-mysql usage:

1. Create MySQl DB;
2. Note: Worktree and git storage must work with two different folders. So you should create two different fs to initialize git (see example below)

Example:

```go
package mypkg

import (

    "fmt"

    "gopkg.in/src-d/go-git.v4"
    "gopkg.in/src-d/go-git.v4/plumbing/cache"
    "gopkg.in/src-d/go-git.v4/storage/filesystem"
    "github.com/ujent/go-git-mysql/mysqlfs"

)

func initGit() {

    fs, err := mysqlfs.New(connStr, tableName)

    if err != nil {
    	t.Error(err)
    }

    fs1, err := mysqlfs.New(connStr, tableName1)

    if err != nil {
    	t.Error(err)
    }

    s := filesystem.NewStorage(fs, cache.NewObjectLRUDefault())
    r, err := git.Init(s, fs1)

    if err != nil {
    	t.Fatal(err)
    }

    ...

}
```
