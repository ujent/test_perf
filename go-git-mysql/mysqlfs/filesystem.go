// Package mysqlfs provides a billy filesystem base on mysql db
package mysqlfs

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/src-d/go-billy.v4"
	"gopkg.in/src-d/go-billy.v4/helper/chroot"
	"gopkg.in/src-d/go-billy.v4/util"
)

//Mysqlfs - realization of billy.Flesystem based on MySQL
type Mysqlfs struct {
	storage Storage
}

//New creates an instance of billy.Filesystem
func New(db *sql.DB, folderName string) (billy.Filesystem, error) {
	if folderName == "" {
		return nil, errors.New("Folder name can't be empty")
	}

	storage, err := newStorage(db, folderName)

	if err != nil {
		return nil, err
	}

	fs := &Mysqlfs{storage: storage}

	return chroot.New(fs, string(separator)), nil
}

// Create creates the named file with mode 0666 (before umask), truncating
// it if it already exists. If successful, methods on the returned File can
// be used for I/O; the associated file descriptor has mode O_RDWR.
func (fs *Mysqlfs) Create(filename string) (billy.File, error) {
	return fs.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0666)
}

// Open opens the named file for reading. If successful, methods on the
// returned file can be used for reading; the associated file descriptor has
// mode O_RDONLY.
func (fs *Mysqlfs) Open(filename string) (billy.File, error) {
	return fs.OpenFile(filename, os.O_RDONLY, 0)
}

// OpenFile is the generalized open call; most users will use Open or Create
// instead. It opens the named file with specified flag (O_RDONLY etc.) and
// perm, (0666 etc.) if applicable. If successful, methods on the returned
// File can be used for I/O.
func (fs *Mysqlfs) OpenFile(filename string, flag int, perm os.FileMode) (billy.File, error) {
	f, err := fs.storage.GetFile(filename)

	if err != nil {
		return nil, err
	}

	if f == nil {
		if !isCreate(flag) {
			return nil, os.ErrNotExist
		}

		var err error
		f, err = fs.storage.NewFile(filename, perm, flag)

		if err != nil {
			return nil, err
		}

	} else {
		if target, isLink := fs.resolveLink(filename, f); isLink {
			return fs.OpenFile(target, flag, perm)
		}
	}

	if f.Mode.IsDir() {
		return nil, fmt.Errorf("cannot open directory: %s", filename)
	}

	return f.Duplicate(perm, flag), nil
}

func (fs *Mysqlfs) resolveLink(fullpath string, f *File) (target string, isLink bool) {
	if !isSymlink(f.Mode) {
		return fullpath, false
	}

	target = string(f.Content)
	if !isAbs(target) {
		target = fs.Join(filepath.Dir(fullpath), target)
	}

	return target, true
}

// On Windows OS, IsAbs validates if a path is valid based on if stars with a
// unit (eg.: `C:\`)  to assert that is absolute, but in this mem implementation
// any path starting by `separator` is also considered absolute.
func isAbs(path string) bool {
	return filepath.IsAbs(path) || strings.HasPrefix(path, string(separator))
}

// Stat returns a FileInfo describing the named file.
func (fs *Mysqlfs) Stat(filename string) (os.FileInfo, error) {
	f, err := fs.storage.GetFile(filename)

	if err != nil {
		return nil, err
	}

	if f == nil {
		return nil, os.ErrNotExist
	}

	fi, err := f.Stat()

	if err != nil {
		return nil, err
	}

	if target, isLink := fs.resolveLink(filename, f); isLink {
		fi, err = fs.Stat(target)
		if err != nil {
			return nil, err
		}
	}

	// the name of the file should always the name of the stated file, so we
	// overwrite the Stat returned from the storage with it, since the
	// filename may belong to a link.
	fi.(*FileInfo).FileName = filepath.Base(filename)

	return fi, nil
}

// Rename renames (moves) oldpath to newpath. If newpath already exists and
// is not a directory, Rename replaces it. OS-specific restrictions may
// apply when oldpath and newpath are in different directories.
func (fs *Mysqlfs) Rename(oldpath, newpath string) error {
	return fs.storage.RenameFile(oldpath, newpath)
}

// Remove removes the named file or directory.
func (fs *Mysqlfs) Remove(filename string) error {
	return fs.storage.RemoveFile(filename)
}

// Join joins any number of path elements into a single path, adding a
// Separator if necessary. Join calls filepath.Clean on the result; in
// particular, all empty strings are ignored. On Windows, the result is a
// UNC path if and only if the first path element is a UNC path.
func (*Mysqlfs) Join(elem ...string) string {
	return filepath.Join(elem...)
}

// TempFile creates a new temporary file in the directory dir with a name
// beginning with prefix, opens the file for reading and writing, and
// returns the resulting *os.File. If dir is the empty string, TempFile
// uses the default directory for temporary files (see os.TempDir).
// Multiple programs calling TempFile simultaneously will not choose the
// same file. The caller can use f.Name() to find the pathname of the file.
// It is the caller's responsibility to remove the file when no longer
// needed.
func (fs *Mysqlfs) TempFile(dir, prefix string) (billy.File, error) {
	return util.TempFile(fs, dir, prefix)
}

// ReadDir reads the directory named by dirname and returns a list of
// directory entries sorted by filename.
func (fs *Mysqlfs) ReadDir(path string) ([]os.FileInfo, error) {
	f, err := fs.storage.GetFile(path)

	if err != nil {
		return nil, err
	}

	if f != nil {
		if target, isLink := fs.resolveLink(path, f); isLink {
			return fs.ReadDir(target)
		}
	}

	var entries []os.FileInfo
	children, err := fs.storage.Children(path)

	if err != nil {
		return nil, err
	}

	for _, f := range children {
		fi, _ := f.Stat()
		entries = append(entries, fi)
	}

	return entries, nil
}

// MkdirAll creates a directory named path, along with any necessary
// parents, and returns nil, or else returns an error. The permission bits
// perm are used for all directories that MkdirAll creates. If path is/
// already a directory, MkdirAll does nothing and returns nil.
func (fs *Mysqlfs) MkdirAll(path string, perm os.FileMode) error {
	_, err := fs.storage.NewFile(path, perm|os.ModeDir, 0)

	return err
}

// Lstat returns a FileInfo describing the named file. If the file is a
// symbolic link, the returned FileInfo describes the symbolic link. Lstat
// makes no attempt to follow the link.
func (fs *Mysqlfs) Lstat(filename string) (os.FileInfo, error) {
	f, err := fs.storage.GetFile(filename)

	if err != nil {
		return nil, err
	}

	if f == nil {
		return nil, os.ErrNotExist
	}

	return f.Stat()
}

// Symlink creates a symbolic-link from link to target. target may be an
// absolute or relative path, and need not refer to an existing node.
// Parent directories of link are created as necessary.
func (fs *Mysqlfs) Symlink(target, link string) error {
	_, err := fs.Stat(link)
	if err == nil {
		return os.ErrExist
	}

	if !os.IsNotExist(err) {
		return err
	}

	return util.WriteFile(fs, link, []byte(target), 0777|os.ModeSymlink)
}

// Readlink returns the target path of link.
func (fs *Mysqlfs) Readlink(link string) (string, error) {
	f, err := fs.storage.GetFile(link)

	if err != nil {
		return "", err
	}

	if f == nil {
		return "", os.ErrNotExist
	}

	if !isSymlink(f.Mode) {
		return "", &os.PathError{
			Op:   "readlink",
			Path: link,
			Err:  fmt.Errorf("not a symlink"),
		}
	}

	return string(f.Content), nil
}

// Capabilities implements the Capable interface.
func (fs *Mysqlfs) Capabilities() billy.Capability {
	return billy.WriteCapability |
		billy.ReadCapability |
		billy.ReadAndWriteCapability |
		billy.SeekCapability |
		billy.TruncateCapability
}

// Name - return file name
func (f *File) Name() string {
	return f.FileName
}

func (f *File) Read(b []byte) (int, error) {
	f1, err := f.storage.GetFile(f.Path)

	if err != nil {
		return 0, err
	}

	f.Content = f1.Content
	n, err := f.ReadAt(b, f.Position)
	f.Position += int64(n)

	if err == io.EOF && n != 0 {
		err = nil
	}

	return n, err
}

// ReadAt reads len(p) bytes into p starting at offset off in the
// underlying input source. It returns the number of bytes
// read (0 <= n <= len(p)) and any error encountered.
func (f *File) ReadAt(b []byte, off int64) (n int, err error) {
	if f.IsClosed {
		return 0, os.ErrClosed
	}

	if !isReadAndWrite(f.Flag) && !isReadOnly(f.Flag) {
		return 0, errors.New("read not supported")
	}

	size := int64(len(f.Content))
	if off >= size {
		return 0, io.EOF
	}

	l := int64(len(b))
	if off+l > size {
		l = size - off
	}

	btr := f.Content[off : off+l]
	if len(btr) < len(b) {
		err = io.EOF
	}
	n = copy(b, btr)

	return n, err
}

func (f *File) WriteAt(p []byte) int {
	off := f.Position
	prev := len(f.Content)

	diff := int(off) - prev

	if diff > 0 {
		f.Content = append(f.Content, make([]byte, diff)...)
	}

	f.Content = append(f.Content[:off], p...)
	if len(f.Content) < prev {
		f.Content = f.Content[:prev]
	}

	return len(p)
}

// Seek sets the offset for the next Read or Write to offset,
// interpreted according to whence:
// SeekStart means relative to the start of the file,
// SeekCurrent means relative to the current offset, and
// SeekEnd means relative to the end.
// Seek returns the new offset relative to the start of the
// file and an error, if any.
func (f *File) Seek(offset int64, whence int) (int64, error) {
	if f.IsClosed {
		return 0, os.ErrClosed
	}

	switch whence {
	case io.SeekCurrent:
		f.Position += offset
	case io.SeekStart:
		f.Position = offset
	case io.SeekEnd:
		f.Position = int64(len(f.Content)) + offset
	}

	return f.Position, nil
}

func (f *File) Write(p []byte) (int, error) {
	if f.IsClosed {
		return 0, os.ErrClosed
	}

	if !isReadAndWrite(f.Flag) && !isWriteOnly(f.Flag) {
		return 0, errors.New("write not supported")
	}

	n := f.WriteAt(p)
	f.Position += int64(n)

	err := f.storage.UpdateFileContent(f.ID, f.Content)

	if err != nil {
		return 0, err
	}

	return n, nil
}

// Close - implementation of the basic Close method.
func (f *File) Close() error {
	if f.IsClosed {
		return os.ErrClosed
	}

	f.IsClosed = true

	return nil
}

// Truncate the file
func (f *File) Truncate(size int64) error {
	if size < int64(len(f.Content)) {
		f.Content = f.Content[:size]
	} else if more := int(size) - len(f.Content); more > 0 {
		f.Content = append(f.Content, make([]byte, more)...)
	}

	return nil
}

func (f *File) Duplicate(mode os.FileMode, flag int) billy.File {
	new := &File{
		ID:       f.ID,
		ParentID: f.ParentID,
		FileName: f.Name(),
		Path:     f.Path,
		Position: f.Position,
		Content:  f.Content,
		Mode:     mode,
		Flag:     flag,
		storage:  f.storage,
	}

	if isAppend(flag) {
		new.Position = int64(len(new.Content))
	}

	if isTruncate(flag) {
		new.Content = make([]byte, 0)
	}

	return new
}

//Stat - get FileInfo from File
func (f *File) Stat() (os.FileInfo, error) {
	return &FileInfo{
		FileName: f.Name(),
		FileMode: f.Mode,
		FileSize: int64(len(f.Content)),
	}, nil
}

// Lock is a no-op in db
func (f *File) Lock() error {
	return nil
}

// Unlock is a no-op in db
func (f *File) Unlock() error {
	return nil
}

func (fi *FileInfo) Name() string {
	return fi.FileName
}

func (fi *FileInfo) Size() int64 {
	return int64(fi.FileSize)
}

func (fi *FileInfo) Mode() os.FileMode {
	return fi.FileMode
}

func (*FileInfo) ModTime() time.Time {
	return time.Now()
}

func (fi *FileInfo) IsDir() bool {
	return fi.FileMode.IsDir()
}

func (*FileInfo) Sys() interface{} {
	return nil
}

func isSymlink(m os.FileMode) bool {
	return m&os.ModeSymlink != 0
}
