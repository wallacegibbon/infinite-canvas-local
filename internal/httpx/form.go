package httpx

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
)

// File is one uploaded file held in memory, the counterpart of multer's file object.
type File struct {
	Field    string // form field name, e.g. image / image[] / videos
	Filename string // client-supplied file name
	Mimetype string // from the part's Content-Type
	Bytes    []byte
}

// FormData is the result of parsing one multipart request.
type FormData struct {
	Values map[string][]string
	Files  []File
}

// MaxUploadFiles mirrors the old multer limits.files = 12.
const MaxUploadFiles = 12

// MaxFileBytes is the default per-file ceiling (50MB). The server layer checks
// the configured value as well.
const MaxFileBytes = 50 << 20

func isMultipart(contentType string) bool {
	return strings.HasPrefix(strings.ToLower(contentType), "multipart/form-data")
}

func isFormURLEncoded(contentType string) bool {
	return strings.HasPrefix(strings.ToLower(contentType), "application/x-www-form-urlencoded")
}

// ParseMultipart streams the multipart body and stops as soon as a file exceeds
// the ceiling, instead of spooling everything to a temp file first.
func ParseMultipart(r *http.Request) (*FormData, error) {
	form := &FormData{Values: map[string][]string{}}
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, BadRequest("invalid_multipart", "not a valid multipart request")
	}

	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, BadRequest("invalid_multipart", "failed to read the multipart body")
		}
		name := part.FormName()
		filename := part.FileName()

		if filename == "" {
			// A plain text field.
			data, readErr := io.ReadAll(io.LimitReader(part, 1<<20))
			_ = part.Close()
			if readErr != nil {
				return nil, BadRequest("invalid_multipart", "failed to read a form field")
			}
			form.Values[name] = append(form.Values[name], string(data))
			continue
		}

		if len(form.Files) >= MaxUploadFiles {
			_ = part.Close()
			return nil, NewError(http.StatusBadRequest, "LIMIT_FILE_COUNT", "at most 12 files per upload")
		}

		data, readErr := io.ReadAll(io.LimitReader(part, MaxFileBytes+1))
		mimetype := part.Header.Get("Content-Type")
		_ = part.Close()
		if readErr != nil {
			return nil, BadRequest("invalid_multipart", "failed to read an uploaded file")
		}
		if int64(len(data)) > MaxFileBytes {
			return nil, NewError(http.StatusRequestEntityTooLarge, "LIMIT_FILE_SIZE", "uploaded file is too large")
		}
		if mimetype == "" {
			mimetype = "application/octet-stream"
		}
		if mediaType, _, err := mime.ParseMediaType(mimetype); err == nil {
			mimetype = mediaType
		}
		form.Files = append(form.Files, File{
			Field:    name,
			Filename: filename,
			Mimetype: mimetype,
			Bytes:    data,
		})
	}
	return form, nil
}
