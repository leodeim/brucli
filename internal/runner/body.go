package runner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/leodeim/brucli/internal/collection"
	"github.com/leodeim/brucli/internal/script"
	"github.com/leodeim/brucli/internal/vars"
)

func graphqlBody(r *collection.Request, d *script.Request, scope *vars.Scope) error {
	var variables map[string]any
	if raw := strings.TrimSpace(scope.Interpolate(r.Body.Variables)); raw != "" {
		if err := json.Unmarshal([]byte(raw), &variables); err != nil {
			return fmt.Errorf("GraphQL variables must be a JSON object: %w", err)
		}
	}
	payload := map[string]any{"query": d.Body}
	if variables != nil {
		payload["variables"] = variables
	}
	if r.Body.OperationName != "" {
		payload["operationName"] = scope.Interpolate(r.Body.OperationName)
	}
	if d.Method == "GET" {
		u, err := url.Parse(d.URL)
		if err != nil {
			return err
		}
		query := u.Query()
		for name, value := range payload {
			if name == "variables" {
				data, _ := json.Marshal(value)
				query.Set(name, string(data))
			} else {
				query.Set(name, value.(string))
			}
		}
		u.RawQuery = query.Encode()
		d.URL, d.Body = u.String(), ""
		return nil
	}
	if d.Method != "POST" {
		return fmt.Errorf("GraphQL requires GET or POST, got %s", d.Method)
	}
	data, err := json.Marshal(payload)
	d.Body = string(data)
	return err
}

// Multipart attachments use paths relative to the collection, as Bruno does.
func (s *Session) multipartBody(r *collection.Request, out *script.Request, scope *vars.Scope) error {
	var data bytes.Buffer
	writer := multipart.NewWriter(&data)
	for _, field := range r.Body.Form {
		if !field.Enabled {
			continue
		}
		name, value := scope.Interpolate(field.Name), scope.Interpolate(field.Value)
		if field.Type != "file" {
			header := textproto.MIMEHeader{}
			header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": name}))
			if field.ContentType != "" {
				header.Set("Content-Type", scope.Interpolate(field.ContentType))
			}
			part, err := writer.CreatePart(header)
			if err != nil {
				return err
			}
			if _, err := io.WriteString(part, value); err != nil {
				return err
			}
			continue
		}
		for _, path := range strings.Split(value, "\n") {
			if path == "" {
				continue
			}
			if !filepath.IsAbs(path) {
				path = filepath.Join(s.Collection.Dir, path)
			}
			file, err := os.Open(path)
			if err != nil {
				return fmt.Errorf("multipart field %s: %w", name, err)
			}
			info, err := file.Stat()
			if err != nil || !info.Mode().IsRegular() {
				file.Close()
				return fmt.Errorf("multipart attachment %q is not a regular file", path)
			}
			if info.Size()+int64(data.Len()) > maxBody {
				file.Close()
				return fmt.Errorf("multipart body exceeds %d MiB", maxBody>>20)
			}
			header := textproto.MIMEHeader{}
			header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": name, "filename": filepath.Base(path)}))
			contentType := scope.Interpolate(field.ContentType)
			if contentType == "" {
				contentType = mime.TypeByExtension(filepath.Ext(path))
			}
			if contentType == "" {
				contentType = "application/octet-stream"
			}
			header.Set("Content-Type", contentType)
			part, err := writer.CreatePart(header)
			if err == nil {
				_, err = io.Copy(part, io.LimitReader(file, maxBody-int64(data.Len())+1))
			}
			closeErr := file.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
			if data.Len() > maxBody {
				return fmt.Errorf("multipart body exceeds %d MiB", maxBody>>20)
			}
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	out.Body = data.String()
	out.SetHeader("Content-Type", writer.FormDataContentType())
	return nil
}
