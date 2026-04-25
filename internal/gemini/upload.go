package gemini

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/url"
	"path"
	"strings"

	http "github.com/bogdanfinn/fhttp"
)

const (
	EndpointUpload = "https://content-push.googleapis.com/upload"
	UploadPushID   = "feeds/mcudyrk2a4khkz"
)

func (c *Client) UploadFile(data []byte, filename string) (string, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return "", fmt.Errorf("failed to create form file: %v", err)
	}

	if _, err := part.Write(data); err != nil {
		return "", fmt.Errorf("failed to write file data: %v", err)
	}

	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("failed to close multipart writer: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, EndpointUpload, &buf)
	if err != nil {
		return "", err
	}

	req.Header.Set("Push-ID", UploadPushID)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("User-Agent", GetCurrentUserAgent())
	req.Header.Set("Origin", "https://gemini.google.com")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("upload failed with status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

func (c *Client) DownloadURL(rawURL string) ([]byte, string, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("User-Agent", GetCurrentUserAgent())
	req.Header.Set("Accept", "*/*")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("download failed with status: %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read response: %v", err)
	}

	filename := filenameFromURL(rawURL)
	ct := resp.Header.Get("Content-Type")
	if ct != "" && filename == "download" {
		if ext := mimeToExt(ct); ext != "" {
			filename = "download" + ext
		}
	}

	return data, filename, nil
}

func (c *Client) DownloadAndUpload(rawURL string) (FileData, error) {
	data, filename, err := c.DownloadURL(rawURL)
	if err != nil {
		return FileData{}, err
	}

	fid, err := c.UploadFile(data, filename)
	if err != nil {
		return FileData{}, err
	}

	return FileData{URL: fid, FileName: filename}, nil
}

func filenameFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "download"
	}
	base := path.Base(u.Path)
	if base == "" || base == "." || base == "/" {
		return "download"
	}
	return base
}

func mimeToExt(mimeType string) string {
	mimeType = strings.Split(mimeType, ";")[0]
	mimeType = strings.TrimSpace(strings.ToLower(mimeType))
	switch mimeType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	case "text/csv":
		return ".csv"
	case "application/json":
		return ".json"
	case "audio/mpeg":
		return ".mp3"
	case "audio/wav":
		return ".wav"
	case "video/mp4":
		return ".mp4"
	default:
		return ""
	}
}

