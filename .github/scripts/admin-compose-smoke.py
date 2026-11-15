#
# Licensed to the Apache Software Foundation (ASF) under one
# or more contributor license agreements.  See the NOTICE file
# distributed with this work for additional information
# regarding copyright ownership.  The ASF licenses this file
# to you under the Apache License, Version 2.0 (the
# "License"); you may not use this file except in compliance
# with the License.  You may obtain a copy of the License at
#
#   http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing,
# software distributed under the License is distributed on an
# "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
# KIND, either express or implied.  See the License for the
# specific language governing permissions and limitations
# under the License.

import json
import socket
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


BASE_URL = "http://127.0.0.1:8088"


def request(path, fields=None, headers=None):
    data = urllib.parse.urlencode(fields).encode() if fields is not None else None
    request_headers = dict(headers or {})
    if fields is not None:
        request_headers["Content-Type"] = "application/x-www-form-urlencoded"

    req = urllib.request.Request(BASE_URL + path, data=data, headers=request_headers)
    try:
        with urllib.request.urlopen(req, timeout=10) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as error:
        raise RuntimeError(f"{path} returned HTTP {error.code}") from error
    except urllib.error.URLError as error:
        raise RuntimeError(f"{path} could not reach the Admin stack") from error


def require_success(response, operation):
    if response.get("code") != "10001":
        raise RuntimeError(f"{operation} returned an Admin error")


def wait_for_tcp(port, timeout=30):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=2):
                return
        except OSError:
            time.sleep(1)
    raise RuntimeError(f"service did not listen on port {port}")


def main():
    status, page = request("/login")
    if status != 200 or b'<div id="root"></div>' not in page:
        raise RuntimeError("Admin login page did not render the React application")
    print("Admin frontend route passed")

    status, swagger = request("/swagger/index.html")
    if status != 200 or b"swagger" not in swagger.lower():
        raise RuntimeError("frontend proxy did not serve the Admin backend Swagger page")
    print("frontend-to-backend proxy passed")

    username = f"ci-smoke-{uuid.uuid4().hex}"
    password = uuid.uuid4().hex
    status, body = request("/register", {"username": username, "password": password})
    if status != 200:
        raise RuntimeError("registration endpoint returned an unexpected HTTP status")
    require_success(json.loads(body), "registration")

    status, body = request("/login", {"username": username, "password": password})
    if status != 200:
        raise RuntimeError("login endpoint returned an unexpected HTTP status")
    login = json.loads(body)
    require_success(login, "login")
    token = login.get("data", {}).get("token")
    if not token:
        raise RuntimeError("login did not return an authentication token")
    print("Admin registration and login passed")

    status, body = request("/config/api/route/list", headers={"token": token})
    if status != 200:
        raise RuntimeError("route list endpoint returned an unexpected HTTP status")
    routes = json.loads(body)
    require_success(routes, "route list")
    if not isinstance(routes.get("data"), list):
        raise RuntimeError("route list did not return a list")
    print("authenticated Admin API and etcd access passed")

    wait_for_tcp(8181)
    wait_for_tcp(8888)
    print("OPA and Pixiu gateway listeners passed")


if __name__ == "__main__":
    main()
