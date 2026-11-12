package rootfs

import (
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const verbatimPrefix = "\\\\?\\"
const verbatimUNC = verbatimPrefix + "UNC\\"

// ExactAbsolutePath avoids GetFullPathName on the final component: that Win32
// API can turn "file.txt " into "file.txt" before the file is opened.
func ExactAbsolutePath(path string) (string, error) {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		if !needsVerbatim(path) {
			return filepath.Abs(path)
		}
		withMarker, err := filepath.Abs(filepath.Join(path, ".cyberagent-exact-path-marker"))
		if err != nil {
			return "", err
		}
		path = filepath.Dir(withMarker)
	}
	if !needsVerbatim(path) || strings.HasPrefix(path, verbatimPrefix) {
		return path, nil
	}
	if strings.HasPrefix(path, "\\\\") {
		return verbatimUNC + strings.TrimPrefix(path, "\\\\"), nil
	}
	return verbatimPrefix + path, nil
}

// OrdinaryPath removes only ordinary Win32 drive/UNC prefixes for lexical
// identity and containment comparisons. Do not use it to open a file.
func OrdinaryPath(path string) string {
	if strings.HasPrefix(path, verbatimUNC) {
		return "\\\\" + strings.TrimPrefix(path, verbatimUNC)
	}
	if rest, ok := strings.CutPrefix(path, verbatimPrefix); ok && len(rest) >= 3 && rest[1] == ':' {
		return rest
	}
	return path
}

func needsVerbatim(path string) bool {
	for _, part := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part != "." && part != ".." && (strings.HasSuffix(part, " ") || strings.HasSuffix(part, ".")) {
			return true
		}
	}
	return false
}

func (r *ExactRoot) Open(name string) (*os.File, error) {
	if !needsVerbatim(name) {
		return r.Root.Open(name)
	}
	return r.openExact(name, windows.FILE_GENERIC_READ, windows.FILE_OPEN, 0, 0)
}

func (r *ExactRoot) Stat(name string) (os.FileInfo, error) {
	if !needsVerbatim(name) {
		return r.Root.Stat(name)
	}
	return r.statExact(name)
}

func (r *ExactRoot) Lstat(name string) (os.FileInfo, error) {
	if !needsVerbatim(name) {
		return r.Root.Lstat(name)
	}
	// Exact opens refuse reparse points instead of traversing them.
	return r.statExact(name)
}

func (r *ExactRoot) statExact(name string) (os.FileInfo, error) {
	file, err := r.openExact(name, windows.FILE_READ_ATTRIBUTES, windows.FILE_OPEN, 0, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return file.Stat()
}

func (r *ExactRoot) CreateExclusive(name string, mode os.FileMode) (*os.File, error) {
	if !needsVerbatim(name) {
		return r.Root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	}
	return r.openExact(name, windows.FILE_GENERIC_WRITE, windows.FILE_CREATE, windows.FILE_NON_DIRECTORY_FILE, mode)
}

func (r *ExactRoot) Mkdir(name string, mode os.FileMode) error {
	if !needsVerbatim(name) {
		return r.Root.Mkdir(name, mode)
	}
	dir, err := r.openExact(name, windows.FILE_GENERIC_READ, windows.FILE_CREATE, windows.FILE_DIRECTORY_FILE, mode)
	if err != nil {
		return err
	}
	return dir.Close()
}

func (r *ExactRoot) Remove(name string) error {
	if !needsVerbatim(name) {
		return r.Root.Remove(name)
	}
	file, err := r.openExact(name, windows.DELETE, windows.FILE_OPEN,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_DELETE_ON_CLOSE, 0)
	if err != nil {
		return err
	}
	return file.Close()
}

func (r *ExactRoot) Link(oldname, newname string) error {
	if !needsVerbatim(oldname) && !needsVerbatim(newname) {
		return r.Root.Link(oldname, newname)
	}
	return r.setExactName(oldname, newname, false)
}

func (r *ExactRoot) Rename(oldname, newname string) error {
	if !needsVerbatim(oldname) && !needsVerbatim(newname) {
		return r.Root.Rename(oldname, newname)
	}
	return r.setExactName(oldname, newname, true)
}

func exactRelativeName(name string) bool {
	return filepath.IsLocal(name) && filepath.Clean(name) == name
}

func (r *ExactRoot) openExact(name string, access, disposition, options uint32, mode os.FileMode) (*os.File, error) {
	name = filepath.FromSlash(name)
	fail := func(err error) (*os.File, error) { return nil, &os.PathError{Op: "openat", Path: name, Err: err} }
	if !exactRelativeName(name) {
		return fail(os.ErrInvalid)
	}
	// Use a handle to the already-open Root, not its original pathname.
	dir, err := r.Root.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	ntName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return fail(err)
	}
	attrs := windows.OBJECT_ATTRIBUTES{
		RootDirectory: windows.Handle(dir.Fd()),
		ObjectName:    ntName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	attrs.Length = uint32(unsafe.Sizeof(attrs))
	fileAttrs := uint32(windows.FILE_ATTRIBUTE_NORMAL)
	if disposition == windows.FILE_CREATE && mode&0o200 == 0 && options&windows.FILE_DIRECTORY_FILE == 0 {
		fileAttrs = windows.FILE_ATTRIBUTE_READONLY
	}
	var handle windows.Handle
	err = windows.NtCreateFile(&handle, access|windows.SYNCHRONIZE|windows.FILE_READ_ATTRIBUTES,
		&attrs, &windows.IO_STATUS_BLOCK{}, nil, fileAttrs,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, disposition,
		options|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	if err != nil {
		return fail(win32Error(err))
	}
	return os.NewFile(uintptr(handle), name), nil
}

// The rename/link destination is a single exact name relative to an opened
// directory. In particular, no unchecked destination path can traverse a link.
func (r *ExactRoot) setExactName(oldname, newname string, replace bool) error {
	newname = filepath.FromSlash(newname)
	op, access, class := "link", uint32(windows.FILE_WRITE_ATTRIBUTES), uint32(11) // FileLinkInformation
	if replace {
		op, access, class = "rename", windows.DELETE, windows.FileRenameInformation
	}
	fail := func(err error) error { return &os.LinkError{Op: op, Old: oldname, New: newname, Err: err} }
	if newname == "." || !exactRelativeName(newname) {
		return fail(os.ErrInvalid)
	}
	parent := filepath.Dir(newname)
	var destination *os.File
	var err error
	if parent == "." {
		destination, err = r.Root.Open(".")
	} else {
		destination, err = r.openExact(parent, windows.FILE_GENERIC_READ, windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE, 0)
	}
	if err != nil {
		return fail(err)
	}
	defer destination.Close()
	source, err := r.openExact(oldname, access, windows.FILE_OPEN, windows.FILE_NON_DIRECTORY_FILE, 0)
	if err != nil {
		return fail(err)
	}
	defer source.Close()
	// FILE_RENAME_INFORMATION and FILE_LINK_INFORMATION have this same layout.
	info := struct {
		Replace        uint8
		Directory      windows.Handle
		NameByteLength uint32
		Name           [256]uint16
	}{Directory: windows.Handle(destination.Fd())}
	name, err := windows.UTF16FromString(filepath.Base(newname))
	if err != nil || len(name) > len(info.Name) {
		return fail(os.ErrInvalid)
	}
	if replace {
		info.Replace = 1
	}
	copy(info.Name[:], name)
	info.NameByteLength = uint32((len(name) - 1) * 2)
	if err := windows.NtSetInformationFile(windows.Handle(source.Fd()), &windows.IO_STATUS_BLOCK{},
		(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), class); err != nil {
		return fail(win32Error(err))
	}
	return nil
}

func win32Error(err error) error {
	if status, ok := err.(windows.NTStatus); ok {
		return status.Errno()
	}
	return err
}
