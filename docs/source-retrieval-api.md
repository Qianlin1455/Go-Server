# G-2.2 源码检索 API

四个接口均使用 GET，并要求 `Authorization: Bearer {{token}}`。示例中的项目 ID 是 `7`，导入任务 ID 是 `42`，文件 ID 是 `101`。成功响应沿用 `{"code":"SUCCESS","data":...}`。

## 文件列表

`GET /api/v1/projects/7/source-files?revision_sha=0123456789abcdef0123456789abcdef01234567&import_job_id=42&path_prefix=src%2F&page=1&page_size=20`

```json
{
  "code": "SUCCESS",
  "data": {
    "import_job_id": 42,
    "items": [{
      "file_id": 101,
      "import_job_id": 42,
      "revision_sha": "0123456789abcdef0123456789abcdef01234567",
      "path": "src/main.go",
      "language": "go",
      "size_bytes": 13,
      "content_sha256": "df1d036cbbf3df46e2045071e082245ece204c7f53ecf0a4e022bff9bb228f47",
      "created_at": "2026-11-15T02:00:00Z"
    }],
    "total": 1,
    "page": 1,
    "page_size": 20
  }
}
```

`revision_sha` 为必填的 40 位十六进制值。可用 `path` 精确过滤，`path_prefix` 采用区分大小写的字面前缀匹配。两项同时提供时取交集。路径最多 768 字节，前缀可保留末尾的 `/`。

## 文件详情

`GET /api/v1/projects/7/source-files/101`

```json
{
  "code": "SUCCESS",
  "data": {
    "file_id": 101,
    "import_job_id": 42,
    "revision_sha": "0123456789abcdef0123456789abcdef01234567",
    "path": "src/main.go",
    "blob_sha": "06ab7d0f9a35a7d1070711496d6ca1cb892a258f",
    "content_sha256": "df1d036cbbf3df46e2045071e082245ece204c7f53ecf0a4e022bff9bb228f47",
    "language": "go",
    "size_bytes": 13,
    "content": "package main\n"
  }
}
```

文件 ID 定位到唯一快照。源码原文上限是 512 KiB，超限返回 `413 SOURCE_FILE_TOO_LARGE`。

## 文件片段

`GET /api/v1/projects/7/source-files/101/spans?page=1&page_size=20`

```json
{
  "code": "SUCCESS",
  "data": {
    "items": [{
      "span_id": 201,
      "span_index": 0,
      "line_start": 1,
      "line_end": 1,
      "content": "package main\n",
      "content_sha256": "df1d036cbbf3df46e2045071e082245ece204c7f53ecf0a4e022bff9bb228f47"
    }],
    "total": 1,
    "page": 1,
    "page_size": 20
  }
}
```

行号从 1 开始，首尾行均计入。空文件返回空片段列表。

## 关键词搜索

`GET /api/v1/projects/7/source-search?revision_sha=0123456789abcdef0123456789abcdef01234567&import_job_id=42&q=package&path_prefix=src%2F&page=1&page_size=20`

```json
{
  "code": "SUCCESS",
  "data": {
    "import_job_id": 42,
    "items": [{
      "source_file_id": 101,
      "source_span_id": 201,
      "import_job_id": 42,
      "revision_sha": "0123456789abcdef0123456789abcdef01234567",
      "path": "src/main.go",
      "language": "go",
      "line_start": 1,
      "line_end": 1,
      "content": "package main\n",
      "content_sha256": "df1d036cbbf3df46e2045071e082245ece204c7f53ecf0a4e022bff9bb228f47"
    }],
    "total": 1,
    "page": 1,
    "page_size": 20
  }
}
```

`q` 必填且最多 256 字节，按区分大小写的字面子串匹配。输入中的 `%`、`_` 和 `!` 都按原字符匹配。查询在单个片段内执行，返回片段的完整行区间。搜索的数据库操作共享 2 秒预算。

## 快照与分页

列表和搜索可用 `import_job_id` 固定快照。省略时选择同一项目、同一 Revision 下 ID 最大的 `succeeded` 任务。从响应的 `data.import_job_id` 取值并用于后续页，可以避免分页期间新任务成功造成快照变化。迁移前的成功任务可对应零条源码记录。

所有接口按 JWT 用户、项目、文件与 ImportJob 的归属关系和成功状态约束查询。显式快照还要与请求的 Revision 一致。资源不存在和无权访问均返回 `404 NOT_FOUND`。

`page` 默认 1，最大 1000。`page_size` 默认 20，文件列表最大 100，片段和搜索最大 20。没有匹配项或页码超过结果末尾时返回 `items: []`。文件按 `path, id` 升序，片段按 `span_index, id` 升序，搜索按 `path, span_index, source_span_id` 升序。现有迁移 `000017` 提供快照内文件与文件内片段的索引。关键词查询在选定快照内扫描片段。

## Apifox 验收

1. 将 `base_url` 设为本地 Go-Server 地址，通过 `POST /api/v1/auth/login` 取得用户 A 的 token。选一个已完成源码导入的项目，记录 `project_id`、`revision_sha` 和 `import_job_id`。
2. 用上面的列表请求获取 `file_id`，检查列表字段。设置 `page_size=1`，保持 `import_job_id` 不变并切到第 2 页，验证顺序与归属。
3. 用该 `file_id` 请求详情和片段，核对原文、行号和 SHA-256。在搜索中填入文件中的词，确认结果的文件 ID、Revision、片段 ID 和行区间均可对应回原文件。需要查找 `%` 或 `_` 时在 Apifox Params 中填原字符。
4. 分别删除 token、将 token 换为用户 B、删除列表的 `revision_sha`、删除搜索的 `q`，期望得到 401、404、400、400。用未完成或失败的任务 ID 固定快照时，期望得到 404。
