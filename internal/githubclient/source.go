package githubclient

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxSourceFileBytes = 512 << 10

type RevisionFile struct {
	Path     string
	BlobSHA  string
	Language string
	Content  []byte
}

type RevisionSource struct {
	CommitSHA string
	TreeSHA   string
	Files     []RevisionFile
}

var sourceLanguages = map[string]string{
	".go": "go", ".rs": "rust", ".py": "python", ".rb": "ruby", ".php": "php",
	".java": "java", ".kt": "kotlin", ".cs": "csharp", ".swift": "swift",
	".c": "c", ".h": "c", ".cpp": "cpp", ".cc": "cpp", ".hpp": "cpp",
	".js": "javascript", ".jsx": "javascript", ".mjs": "javascript",
	".ts": "typescript", ".tsx": "typescript",
	".sh": "shell", ".ps1": "powershell", ".sql": "sql",
	".html": "html", ".css": "css", ".vue": "vue",
	".json": "json", ".yaml": "yaml", ".yml": "yaml", ".toml": "toml",
	".xml": "xml", ".md": "markdown", ".rst": "restructuredtext", ".txt": "text",
}

// DownloadRevision fetches regular text files from one immutable Git tree.
// It downloads at most one blob at a time.
func (c *Client) DownloadRevision(ctx context.Context, owner, repo, revisionSHA string) (RevisionSource, error) {
	summary, entries, err := c.loadRevisionTree(ctx, owner, repo, revisionSHA)
	if err != nil {
		return RevisionSource{}, err
	}
	var eligible []revisionTreeEntry
	paths := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if !validSourcePath(entry.Path) {
			return RevisionSource{}, fmt.Errorf("%w: invalid tree entry path", ErrBadResponse)
		}
		if _, exists := paths[entry.Path]; exists {
			return RevisionSource{}, fmt.Errorf("%w: duplicate tree entry path", ErrBadResponse)
		}
		paths[entry.Path] = struct{}{}
		if entry.Type != "blob" || (entry.Mode != "100644" && entry.Mode != "100755") ||
			sourceLanguages[strings.ToLower(path.Ext(entry.Path))] == "" {
			continue
		}
		if *entry.Size > maxSourceFileBytes {
			return RevisionSource{}, ErrTreeBudgetExceeded
		}
		if !shaPattern.MatchString(entry.SHA) {
			return RevisionSource{}, fmt.Errorf("%w: invalid tree blob SHA", ErrBadResponse)
		}
		eligible = append(eligible, entry)
	}

	result := RevisionSource{CommitSHA: summary.CommitSHA, TreeSHA: summary.TreeSHA}
	for _, entry := range eligible {
		if err := ctx.Err(); err != nil {
			return RevisionSource{}, err
		}
		sha := strings.ToLower(entry.SHA)
		content, err := c.downloadBlob(ctx, owner, repo, sha, *entry.Size)
		if err != nil {
			return RevisionSource{}, err
		}
		if !isSourceText(content) {
			continue
		}
		result.Files = append(result.Files, RevisionFile{
			Path: entry.Path, BlobSHA: sha,
			Language: sourceLanguages[strings.ToLower(path.Ext(entry.Path))],
			Content:  content,
		})
	}
	return result, nil
}

func validSourcePath(name string) bool {
	return name != "." && len(name) <= 768 && fs.ValidPath(name) &&
		!strings.ContainsAny(name, `\:<>"|?*`) && strings.IndexFunc(name, unicode.IsControl) < 0
}

func (c *Client) downloadBlob(ctx context.Context, owner, repo, sha string, size int64) ([]byte, error) {
	var blob struct {
		SHA      string  `json:"sha"`
		Size     *int64  `json:"size"`
		Encoding string  `json:"encoding"`
		Content  *string `json:"content"`
	}
	// Allow base64, escaped newlines and JSON metadata within a fixed bound.
	const maxBlobResponseBytes = 2 * maxSourceFileBytes
	err := c.doJSON(ctx, "/repos/"+owner+"/"+repo+"/git/blobs/"+sha, maxBlobResponseBytes, &blob)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(blob.SHA, sha) || blob.Size == nil || *blob.Size != size ||
		blob.Encoding != "base64" || blob.Content == nil {
		return nil, fmt.Errorf("%w: invalid blob metadata", ErrBadResponse)
	}
	content, err := base64.StdEncoding.Strict().DecodeString(*blob.Content)
	if err != nil || int64(len(content)) != size {
		return nil, fmt.Errorf("%w: invalid blob content or size", ErrBadResponse)
	}
	checksum := sha1.New() // Git object ID, separate from the persisted SHA-256.
	fmt.Fprintf(checksum, "blob %d\x00", len(content))
	checksum.Write(content)
	if hex.EncodeToString(checksum.Sum(nil)) != sha {
		return nil, fmt.Errorf("%w: Git blob content SHA mismatch", ErrBadResponse)
	}
	return content, nil
}

func isSourceText(content []byte) bool {
	return utf8.Valid(content) && bytes.IndexFunc(content, func(r rune) bool {
		return unicode.IsControl(r) && r != '\t' && r != '\n' && r != '\r'
	}) < 0
}
