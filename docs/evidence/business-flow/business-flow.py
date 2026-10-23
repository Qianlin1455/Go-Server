"""Run against a locally started server. Uses only Python's standard library.
Creates fresh demo users and data; leaves them available for Apifox inspection.
Never prints passwords or access tokens. Output is actual HTTP evidence.
"""
import json
import sys
import uuid
from urllib.request import Request, urlopen
from urllib.error import HTTPError

base = (sys.argv[1] if len(sys.argv) > 1 else "http://localhost:8080").rstrip("/")
token = ""


def call(method, path, expected=200, body=None):
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    request = Request(base + path, data=json.dumps(body).encode() if body is not None else None,
                      headers=headers, method=method)
    try:
        response = urlopen(request, timeout=10)
    except HTTPError as error:
        response = error
    raw = response.read().decode()
    result = json.loads(raw) if raw else None
    safe_body = dict(body) if body else None
    if safe_body and "password" in safe_body:
        safe_body["password"] = "<REDACTED>"
    safe_result = json.loads(raw) if raw else None
    if path.endswith("/login") and safe_result and "data" in safe_result:
        safe_result["data"]["access_token"] = "<REDACTED>"
    print(method, path, "=>", response.status)
    if safe_body:
        print("request:", json.dumps(safe_body, ensure_ascii=False))
    print("response:", json.dumps(safe_result, ensure_ascii=False))
    assert response.status == expected, (expected, response.status)
    return result["data"] if result and "data" in result else result


def account():
    suffix = uuid.uuid4().hex[:20]
    password = uuid.uuid4().hex
    email = "p0_" + suffix + "@example.com"
    call("POST", "/api/v1/auth/register", 201,
         {"username": "p0_" + suffix, "email": email, "password": password})
    return call("POST", "/api/v1/auth/login", body={"email": email, "password": password})["access_token"]


call("GET", "/healthz")
token = account()
token_a = token
project = call("POST", "/api/v1/projects", 201, {"name": "P0 business flow " + uuid.uuid4().hex[:8]})
pid = project["id"]
conversation = call("POST", "/api/v1/conversations", 201, {"title": "Transaction practice", "project_id": pid})
cid = conversation["id"]
message = call("POST", f"/api/v1/conversations/{cid}/messages", 201, {"content": "Verify atomic task status and audit updates"})
task = call("POST", f"/api/v1/projects/{pid}/tasks", 201,
            {"title": "Practice transaction rollback", "source_message_id": message["id"]})
tid = task["id"]
path = f"/api/v1/tasks/{tid}"
call("PATCH", path + "/status", 409, {"status": "done", "version": task["version"]})
call("PATCH", path + "/status", 204, {"status": "doing", "version": task["version"]})
current = call("GET", f"/api/v1/projects/{pid}/tasks?status=doing")["items"][0]
call("PATCH", path + "/status", 409, {"status": "done", "version": current["version"]})
call("POST", path + "/evidence", 201, {"type": "test_output", "text": "Draft observation", "status": "draft"})
call("PATCH", path + "/status", 409, {"status": "done", "version": current["version"]})
call("POST", path + "/evidence", 201,
     {"type": "experiment_text", "text": "Observed HTTP: todo to done rejected; doing without submitted evidence rejected, including draft-only evidence.", "status": "submitted"})
call("PATCH", path + "/status", 409, {"status": "done", "version": task["version"]})
call("PATCH", path + "/status", 204, {"status": "done", "version": current["version"]})
page = call("GET", f"/api/v1/projects/{pid}/tasks?page=1&page_size=20&status=done")
assert page["total"] == 1 and len(page["items"]) == 1
empty = call("GET", f"/api/v1/projects/{pid}/tasks?page=2&page_size=20&status=done")
assert empty["total"] == 1 and empty["items"] == []
call("GET", f"/api/v1/projects/{pid}/tasks?status=invalid", 400)
recap = call("GET", path + "/retrospective")
assert recap["task"]["status"] == "done" and recap["task"]["version"] == 3
assert recap["task"]["completed_at"] and recap["source"]["id"] == message["id"]
assert len(recap["evidence"]) == 2 and recap["evidence"][1]["status"] == "submitted"
assert [(a["from_status"], a["to_status"], a["to_version"]) for a in recap["audits"]] == [("todo", "doing", 2), ("doing", "done", 3)]
token = account()
call("GET", path + "/retrospective", 404)
call("GET", f"/api/v1/projects/{pid}/tasks", 404)
token = token_a
print(f"PASS: persisted project={pid} conversation={cid} message={message['id']} task={tid}; JWT and passwords redacted")
