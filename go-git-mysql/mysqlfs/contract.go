package mysqlfs

import (
	"database/sql"
	"os"
)

// Storage - an interface of storage working with files in db
type Storage interface {
	NewFile(path string, mode os.FileMode, flag int) (*File, error)
	GetFile(path string) (*File, error)
	GetFileID(path string) (int64, error)
	RenameFile(from, to string) error
	RemoveFile(path string) error
	Children(path string) ([]*File, error)
	ChildrenIdsByFileID(id int64) ([]int64, error)
	ChildrenByFileID(id int64) ([]*File, error)
	CreateParentAddToFile(path string, mode os.FileMode, f *File) error

	UpdateFileContent(fileID int64, content []byte) error
}

//FileDB - main db obect for saving files
type FileDB struct {
	ID       int64         `db:"id"`
	ParentID sql.NullInt64 `db:"parentID"`
	Name     string        `db:"name"`
	Path     string        `db:"path"`
	Content  []byte        `db:"content"`
	Flag     int           `db:"flag"`
	Mode     int64         `db:"mode"`
}

//File - Mysql fs object, realizes interface billy.File
type File struct {
	ID       int64
	ParentID int64
	FileName string
	Path     string
	Content  []byte
	Position int64
	Flag     int
	Mode     os.FileMode

	IsClosed bool
	storage  *storage
}

// FileInfo - wrapper on os.FileMode with additional info
type FileInfo struct {
	FileID   int64
	FileName string
	FileSize int64
	FileMode os.FileMode
}
