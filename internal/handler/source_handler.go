package handler

import (
	"net/http"
	"net/url"
	"strconv"

	"GoServer/internal/midware"
	"GoServer/internal/service"

	"github.com/gin-gonic/gin"
)

type SourceHandler struct {
	svc *service.SourceService
}

func NewSourceHandler(svc *service.SourceService) *SourceHandler { return &SourceHandler{svc: svc} }

func (h *SourceHandler) RegisterRoutes(private *gin.RouterGroup) {
	private.GET("/projects/:id/source-files", h.ListFiles)
	private.GET("/projects/:id/source-files/:file_id", h.GetFile)
	private.GET("/projects/:id/source-files/:file_id/spans", h.ListSpans)
	private.GET("/projects/:id/source-search", h.Search)
}

func (h *SourceHandler) ListFiles(c *gin.Context) {
	userID, projectID, _, ok := sourceRequestIDs(c, false)
	if !ok {
		return
	}
	input, _, ok := sourceQuery(c)
	if !ok {
		return
	}
	files, total, jobID, err := h.svc.ListFiles(c.Request.Context(), userID, projectID, input)
	if err != nil {
		WriteServiceerror(c, err)
		return
	}
	items := make([]gin.H, 0, len(files))
	for _, file := range files {
		items = append(items, gin.H{
			"file_id": file.ID, "import_job_id": file.ImportJobID, "revision_sha": file.RevisionSHA,
			"path": file.Path, "language": file.Language, "size_bytes": file.SizeBytes,
			"content_sha256": file.ContentSHA256, "created_at": file.CreatedAt,
		})
	}
	data := sourcePage(items, total, input)
	data["import_job_id"] = jobID
	writeSourceSuccess(c, data)
}

func (h *SourceHandler) GetFile(c *gin.Context) {
	userID, projectID, fileID, ok := sourceRequestIDs(c, true)
	if !ok {
		return
	}
	file, err := h.svc.GetFile(c.Request.Context(), userID, projectID, fileID)
	if err != nil {
		WriteServiceerror(c, err)
		return
	}
	writeSourceSuccess(c, gin.H{
		"file_id": file.ID, "import_job_id": file.ImportJobID, "revision_sha": file.RevisionSHA,
		"path": file.Path, "blob_sha": file.BlobSHA, "content_sha256": file.ContentSHA256,
		"language": file.Language, "size_bytes": file.SizeBytes, "content": file.Content,
	})
}

func (h *SourceHandler) ListSpans(c *gin.Context) {
	userID, projectID, fileID, ok := sourceRequestIDs(c, true)
	if !ok {
		return
	}
	input, _, ok := sourceQuery(c)
	if !ok {
		return
	}
	spans, total, err := h.svc.ListSpans(c.Request.Context(), userID, projectID, fileID, input.Page, input.PageSize)
	if err != nil {
		WriteServiceerror(c, err)
		return
	}
	items := make([]gin.H, 0, len(spans))
	for _, span := range spans {
		items = append(items, gin.H{
			"span_id": span.ID, "span_index": span.SpanIndex, "line_start": span.LineStart,
			"line_end": span.LineEnd, "content": span.Content, "content_sha256": span.ContentSHA256,
		})
	}
	writeSourceSuccess(c, sourcePage(items, total, input))
}

func (h *SourceHandler) Search(c *gin.Context) {
	userID, projectID, _, ok := sourceRequestIDs(c, false)
	if !ok {
		return
	}
	input, keyword, ok := sourceQuery(c)
	if !ok {
		return
	}
	matches, total, jobID, err := h.svc.Search(c.Request.Context(), userID, projectID, input, keyword)
	if err != nil {
		WriteServiceerror(c, err)
		return
	}
	items := make([]gin.H, 0, len(matches))
	for _, match := range matches {
		items = append(items, gin.H{
			"source_file_id": match.SourceFileID, "source_span_id": match.ID, "import_job_id": match.ImportJobID,
			"revision_sha": match.RevisionSHA, "path": match.Path, "language": match.Language,
			"line_start": match.LineStart, "line_end": match.LineEnd, "content": match.Content,
			"content_sha256": match.ContentSHA256,
		})
	}
	data := sourcePage(items, total, input)
	data["import_job_id"] = jobID
	writeSourceSuccess(c, data)
}

func sourceRequestIDs(c *gin.Context, needsFileID bool) (userID, projectID, fileID uint64, ok bool) {
	userID, ok = midware.UserIDFromContext(c.Request.Context())
	if !ok {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": "UNAUTHORIZED", "message": "用户身份无效"})
		return 0, 0, 0, false
	}
	projectID, ok = parseTaskPathID(c, "id")
	if ok && needsFileID {
		fileID, ok = parseTaskPathID(c, "file_id")
	}
	return userID, projectID, fileID, ok
}

func sourceQuery(c *gin.Context) (service.SourceQuery, string, bool) {
	params, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		WriteServiceerror(c, service.ErrInvalidSourceInput)
		return service.SourceQuery{}, "", false
	}
	input := service.SourceQuery{
		RevisionSHA: params.Get("revision_sha"), Path: params.Get("path"), PathPrefix: params.Get("path_prefix"),
		Page: 1, PageSize: service.SourceDefaultPageSize,
	}
	if params.Has("page") {
		input.Page, _ = strconv.Atoi(params.Get("page"))
	}
	if params.Has("page_size") {
		input.PageSize, _ = strconv.Atoi(params.Get("page_size"))
	}
	if params.Has("import_job_id") {
		input.ImportJobID, err = strconv.ParseUint(params.Get("import_job_id"), 10, 64)
		if err != nil || input.ImportJobID == 0 {
			WriteServiceerror(c, service.ErrInvalidSourceInput)
			return service.SourceQuery{}, "", false
		}
	}
	return input, params.Get("q"), true
}

func sourcePage(items []gin.H, total int64, input service.SourceQuery) gin.H {
	return gin.H{"items": items, "total": total, "page": input.Page, "page_size": input.PageSize}
}

func writeSourceSuccess(c *gin.Context, data gin.H) {
	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusOK, gin.H{"code": "SUCCESS", "data": data})
}
