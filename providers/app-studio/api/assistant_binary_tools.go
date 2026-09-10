/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

// Binary placement tools. The model cannot author binary bytes, so these two
// write-risk tools place a file the user or the web already has into the
// project workspace: import_attachment copies an attachment from the chat,
// download_file fetches a direct file URL. Both are workspace mutations with
// the same permission, approval, dirty-path, and development-sync handling
// as create_file.
//
// Run-sandbox mode: the per-run coding sandbox's mutate verb is text-only and
// its checkpoint fences the local workspace by source revision, so a binary
// written beside it would fail the run's checkpoint. Both tools therefore
// refuse while a run sandbox is active and say how the user can add the file
// instead (the Code tab upload). They are available in the default mode.

const (
	projectAssistantDownloadTimeout      = 60 * time.Second
	projectAssistantDownloadMaxRedirects = 5
	projectAssistantBinaryToolSandboxMsg = "binary files cannot be placed while this run uses an isolated coding sandbox; ask the user to upload the file in the Code tab (or attach it again after this run) instead"
)

// projectAssistantDownloadHTTPClient reaches public destinations only. Tests
// replace it to reach a local server.
var projectAssistantDownloadHTTPClient = newProjectAssistantDownloadClient()

func newProjectAssistantDownloadClient() *http.Client {
	return &http.Client{
		Timeout: projectAssistantDownloadTimeout,
		Transport: &http.Transport{
			// No proxy: a proxy would dial on the model's behalf and bypass the
			// dial guard.
			Proxy:                 nil,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, Control: webDialGuard}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		},
		CheckRedirect: projectAssistantDownloadCheckRedirect,
	}
}

func projectAssistantDownloadCheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > projectAssistantDownloadMaxRedirects {
		return fmt.Errorf("stopped after %d redirects", projectAssistantDownloadMaxRedirects)
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return fmt.Errorf("redirect to unsupported scheme %q", req.URL.Scheme)
	}
	return nil
}

func projectAssistantImportAttachmentTool(server *Server) projectAssistantTool {
	return projectAssistantToolFunc{
		spec: projectAssistantToolSpec{
			Name: projectToolImportAttachment,
			Description: "Copy a file the user attached to this conversation into the project workspace at a project-relative path — the way to add images, 3D models (.glb/.gltf), fonts, audio, archives, or any other binary the user provides. " +
				"Use the attachmentID from the attachment notice. For a Vite app, static assets usually belong under public/ (e.g. public/assets/jeep.glb, served at /assets/jeep.glb). " +
				"Fails if the path exists unless overwrite is true. The file is then committed to git and synced to the development sandbox like any other edit.",
			Parameters: json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{"attachmentID":{"type":"string","minLength":1,"maxLength":%d},"path":{"type":"string","minLength":1,"maxLength":%d},"overwrite":{"type":"boolean","description":"Replace an existing file at path."},"recoveryOf":{"type":"string","minLength":1,"maxLength":120,"description":"Optional server-issued action reference used only to correlate a retry in the activity feed."}},"required":["attachmentID","path"],"additionalProperties":false}`, projectAssistantAttachmentMaxIDBytes, workspace.MaxProjectPathBytes)),
			Risk:       projectAssistantToolRiskWrite,
		},
		call: func(ctx context.Context, req projectAssistantToolCallRequest) (string, error) {
			s, err := projectAssistantToolServer(server)
			if err != nil {
				return "", err
			}
			if err := projectAssistantRefuseBinaryInRunSandbox(ctx, req); err != nil {
				return "", err
			}
			if req.AttachmentReader == nil {
				return "", errors.New("assistant attachment reader is not configured")
			}
			id, _ := projectToolRawString(req.Arguments["attachmentID"])
			targetPath, _ := projectToolRawString(req.Arguments["path"])
			overwrite, _ := req.Arguments["overwrite"].(bool)
			receipt, err := projectAssistantAttachmentReceiptForID(req, id)
			if err != nil {
				return "", err
			}
			read, err := req.AttachmentReader.ReadAttachment(ctx, req.AttachmentScope, receipt, req.Identity.user, 0, store.AttachmentMaxBytes+1)
			if err != nil {
				return "", fmt.Errorf("read attachment %q: %w", receipt.ID, err)
			}
			if !read.Complete || len(read.Content) > store.AttachmentMaxBytes {
				return "", fmt.Errorf("attachment %q was not returned as one complete bounded object", receipt.ID)
			}
			if err := projectAssistantValidateAttachmentBytes(receipt, read.Content); err != nil {
				return "", err
			}
			result, err := projectAssistantPlaceBinaryFile(ctx, s, req, targetPath, read.Content, overwrite)
			if err != nil {
				return "", err
			}
			return projectAssistantToolJSONResult(projectAssistantBinaryPlacementResult{
				MutationResult: projectAssistantMutationWithOperation(result, projectToolImportAttachment),
				ContentType:    receipt.ContentType,
				SHA256:         receipt.SHA256,
				AttachmentID:   receipt.ID,
			}, nil)
		},
	}
}

func projectAssistantDownloadFileTool(server *Server) projectAssistantTool {
	return projectAssistantToolFunc{
		spec: projectAssistantToolSpec{
			Name: projectToolDownloadFile,
			Description: "Download one file from a direct http(s) file URL (up to 25 MiB) into the project workspace at a project-relative path — for binary assets such as images, 3D models (.glb), fonts, or audio. " +
				"The URL must return the file itself: web pages such as marketplace, gallery, or model-listing pages (Sketchfab, TurboSquid, GitHub blob pages, Google Drive previews) are NOT files; find the direct download/raw URL (or ask the user to attach the file) instead. " +
				"Internal addresses are blocked. Fails if the path exists unless overwrite is true. The file is then committed to git and synced to the development sandbox like any other edit.",
			Parameters: json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{"url":{"type":"string","minLength":1,"maxLength":4096,"description":"Direct http(s) URL of the file itself, not a web page about it."},"path":{"type":"string","minLength":1,"maxLength":%d},"overwrite":{"type":"boolean","description":"Replace an existing file at path."},"recoveryOf":{"type":"string","minLength":1,"maxLength":120,"description":"Optional server-issued action reference used only to correlate a retry in the activity feed."}},"required":["url","path"],"additionalProperties":false}`, workspace.MaxProjectPathBytes)),
			Risk:       projectAssistantToolRiskWrite,
		},
		call: func(ctx context.Context, req projectAssistantToolCallRequest) (string, error) {
			s, err := projectAssistantToolServer(server)
			if err != nil {
				return "", err
			}
			if err := projectAssistantRefuseBinaryInRunSandbox(ctx, req); err != nil {
				return "", err
			}
			rawURL, _ := projectToolRawString(req.Arguments["url"])
			targetPath, _ := projectToolRawString(req.Arguments["path"])
			overwrite, _ := req.Arguments["overwrite"].(bool)
			clean, err := workspace.CleanProjectPath(targetPath)
			if err != nil {
				return "", err
			}
			download, err := projectAssistantDownload(ctx, rawURL, clean)
			if err != nil {
				return "", err
			}
			result, err := projectAssistantPlaceBinaryFile(ctx, s, req, clean, download.data, overwrite)
			if err != nil {
				return "", err
			}
			sum := sha256.Sum256(download.data)
			return projectAssistantToolJSONResult(projectAssistantBinaryPlacementResult{
				MutationResult: projectAssistantMutationWithOperation(result, projectToolDownloadFile),
				ContentType:    download.contentType,
				SHA256:         hex.EncodeToString(sum[:]),
				URL:            download.finalURL,
			}, nil)
		},
	}
}

// projectAssistantBinaryPlacementResult is the tool result: the workspace
// mutation (operation, path, size, version, binary, created, changed) plus
// the source description.
type projectAssistantBinaryPlacementResult struct {
	workspace.MutationResult
	ContentType  string `json:"contentType,omitempty"`
	SHA256       string `json:"sha256"`
	AttachmentID string `json:"attachmentID,omitempty"`
	URL          string `json:"url,omitempty"`
}

func projectAssistantMutationWithOperation(result workspace.MutationResult, operation string) workspace.MutationResult {
	result.Operation = operation
	return result
}

func projectAssistantRefuseBinaryInRunSandbox(ctx context.Context, req projectAssistantToolCallRequest) error {
	sandbox, err := ensureProjectAssistantRunSandboxForRequest(ctx, req)
	if err != nil {
		return err
	}
	if sandbox != nil {
		return errors.New(projectAssistantBinaryToolSandboxMsg)
	}
	return nil
}

// projectAssistantPlaceBinaryFile writes bytes create-only (or as an
// explicit overwrite). Size bounds are the workspace's: 25 MiB for binary
// content, the ordinary text bound for UTF-8 text.
func projectAssistantPlaceBinaryFile(ctx context.Context, s *Server, req projectAssistantToolCallRequest, targetPath string, data []byte, overwrite bool) (workspace.MutationResult, error) {
	result, err := s.workspaces.PutFile(ctx, req.WorkspaceScope, workspace.PutOptions{Path: targetPath, Data: data, CreateOnly: !overwrite})
	if err != nil {
		var mutationErr *workspace.MutationError
		if errors.As(err, &mutationErr) && mutationErr.Code == workspace.MutationErrorTargetExists {
			return workspace.MutationResult{}, fmt.Errorf("%s already exists; pass overwrite=true to replace it or choose another path", mutationErr.Path)
		}
		var tooLarge *workspace.FileTooLargeError
		if errors.As(err, &tooLarge) && !tooLarge.Binary {
			return workspace.MutationResult{}, fmt.Errorf("%s is a %d-byte text file; text files in the workspace are limited to %d bytes", tooLarge.Path, tooLarge.Size, tooLarge.Limit)
		}
		return workspace.MutationResult{}, err
	}
	// A whole-file placement carries no reviewable diff.
	result.Diff = ""
	return result, nil
}

type projectAssistantDownloadedFile struct {
	data        []byte
	contentType string
	finalURL    string
}

// projectAssistantDownload fetches one direct file URL within the bounds.
func projectAssistantDownload(ctx context.Context, rawURL, targetPath string) (projectAssistantDownloadedFile, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return projectAssistantDownloadedFile{}, errors.New("url must be an absolute http(s) URL")
	}
	if u.User != nil {
		return projectAssistantDownloadedFile{}, errors.New("url must not embed credentials")
	}
	ctx, cancel := context.WithTimeout(ctx, projectAssistantDownloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return projectAssistantDownloadedFile{}, err
	}
	req.Header.Set("User-Agent", "faros-app-studio/0.1 (+https://github.com/faroshq/faros)")
	resp, err := projectAssistantDownloadHTTPClient.Do(req)
	if err != nil {
		return projectAssistantDownloadedFile{}, fmt.Errorf("download %s: %w", u.Redacted(), err)
	}
	defer func() { _ = resp.Body.Close() }()
	finalURL := resp.Request.URL.Redacted()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return projectAssistantDownloadedFile{}, fmt.Errorf("download %s: HTTP %d", finalURL, resp.StatusCode)
	}
	if resp.ContentLength > workspace.MaxBinaryWriteBytes {
		return projectAssistantDownloadedFile{}, fmt.Errorf("download %s: file is %d bytes; the limit is %d", finalURL, resp.ContentLength, workspace.MaxBinaryWriteBytes)
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	mediaType, _, _ := mime.ParseMediaType(contentType)
	ext := strings.ToLower(path.Ext(targetPath))
	if (mediaType == "text/html" || mediaType == "application/xhtml+xml") && ext != ".html" && ext != ".htm" {
		return projectAssistantDownloadedFile{}, fmt.Errorf("%s returned a web page (%s), not a file; pages such as marketplace or model listings are not downloads — find the direct file URL or ask the user to attach the file", finalURL, mediaType)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, workspace.MaxBinaryWriteBytes+1))
	if err != nil {
		return projectAssistantDownloadedFile{}, fmt.Errorf("download %s: %w", finalURL, err)
	}
	if len(data) > workspace.MaxBinaryWriteBytes {
		return projectAssistantDownloadedFile{}, fmt.Errorf("download %s: file exceeds the %d-byte limit", finalURL, workspace.MaxBinaryWriteBytes)
	}
	if len(data) == 0 {
		return projectAssistantDownloadedFile{}, fmt.Errorf("download %s: the response was empty", finalURL)
	}
	return projectAssistantDownloadedFile{data: data, contentType: mediaType, finalURL: finalURL}, nil
}
