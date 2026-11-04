package collection

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ErrExists reports a request name whose file is already taken.
var ErrExists = errors.New("a request file with that name already exists")

// NewRequest creates a GET request file at the end of f.
func (f *Folder) NewRequest(name, url string) (*Request, error) {
	doc := mapping()
	r := &Request{
		Name: name, Seq: f.nextSeq(), Method: "GET", URL: url,
		Auth:     Auth{Mode: "inherit"},
		Body:     Body{Mode: "none"},
		Settings: Settings{EncodeURL: true, FollowRedirects: true, MaxRedirects: 5},
		Folder:   f,
		doc:      doc,
	}
	info := mapping()
	add(info, "name", scalar(name))
	add(info, "type", scalar("http"))
	add(info, "seq", integer(r.Seq))
	add(doc, "info", info)
	settings := mapping()
	add(settings, "encodeUrl", boolean(true))
	add(settings, "timeout", integer(0))
	add(doc, "settings", settings)
	return r, f.adopt(r)
}

// Duplicate copies r under a new name at the end of its folder.
func (r *Request) Duplicate(name string) (*Request, error) {
	c := r.Clone()
	c.Name, c.Seq, c.doc = name, r.Folder.nextSeq(), deepCopy(r.doc)
	set(child(c.doc, "info", requestKeys), "seq", integer(c.Seq), infoKeys)
	return c, r.Folder.adopt(c)
}

// adopt gives r a file named after it in f and adds it to f's requests.
func (f *Folder) adopt(r *Request) error {
	path, err := f.freePath(r.Name)
	if err != nil {
		return err
	}
	r.Path, r.Folder = path, f
	if err := r.Save(); err != nil {
		return err
	}
	f.Requests = append(f.Requests, r)
	return nil
}

// Rename changes the request's name and moves its file to match.
func (r *Request) Rename(name string) error {
	path, err := r.Folder.freePath(name)
	if err != nil && !sameFile(path, r.Path) {
		return err
	}
	old := r.Name
	r.Name = name
	if err := r.Save(); err != nil {
		r.Name = old
		return err
	}
	if path == r.Path {
		return nil
	}
	if err := os.Rename(r.Path, path); err != nil {
		return err
	}
	r.Path = path
	return nil
}

// Delete removes the request's file and drops it from its folder.
func (r *Request) Delete() error {
	if err := os.Remove(r.Path); err != nil {
		return err
	}
	r.Folder.Requests = slices.DeleteFunc(r.Folder.Requests, func(x *Request) bool { return x == r })
	return nil
}

func (f *Folder) nextSeq() int {
	seq := 0
	for _, r := range f.Requests {
		seq = max(seq, r.Seq)
	}
	return seq + 1
}

// freePath is the file for a request called name in f; the error reports a name that is taken or unusable.
func (f *Folder) freePath(name string) (string, error) {
	base := strings.Map(func(c rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, c) || c < ' ' {
			return '-'
		}
		return c
	}, strings.TrimSpace(name))
	// A leading dot would make a hidden file, which loading skips.
	if base == "" || base[0] == '.' || strings.EqualFold(base, "folder") {
		return "", fmt.Errorf("unusable request name %q (no leading dot, not \"folder\")", name)
	}
	path := filepath.Join(f.Dir, base+".yml")
	if _, err := os.Stat(path); err == nil {
		return path, fmt.Errorf("%s: %w", filepath.Base(path), ErrExists)
	}
	return path, nil
}

// sameFile reports two paths of one file, as on a case-insensitive file system.
func sameFile(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ia, errA := os.Stat(a)
	ib, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(ia, ib)
}

func integer(n int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprint(n)}
}

func deepCopy(n *yaml.Node) *yaml.Node {
	c := *n
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, ch := range n.Content {
		c.Content[i] = deepCopy(ch)
	}
	return &c
}
