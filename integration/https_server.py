import argparse
import http.server
import json
import pathlib
import ssl
import urllib.parse


class Handler(http.server.SimpleHTTPRequestHandler):
    def __init__(self, *values, directory, github_token, github_manifest):
        self.github_token = github_token
        self.github_manifest = github_manifest
        super().__init__(*values, directory=directory)

    def do_GET(self):
        host = self.headers.get("Host", "").split(":", 1)[0]
        if host == "api.github.com" and self.github_manifest:
            self.serve_github()
            return
        super().do_GET()

    def serve_github(self):
        if self.headers.get("Authorization") != f"Bearer {self.github_token}":
            self.send_error(http.HTTPStatus.UNAUTHORIZED)
            return
        parsed = urllib.parse.urlparse(self.path)
        if parsed.path == "/user":
            self.send_json({"login": "github-user"})
            return
        if parsed.path == "/repos/integration/private":
            self.send_json({"default_branch": "main"})
            return
        if parsed.path == "/repos/integration/private/contents/cpak.json":
            query = urllib.parse.parse_qs(parsed.query)
            if query.get("ref") != ["main"]:
                self.send_error(http.HTTPStatus.BAD_REQUEST)
                return
            self.send_content(pathlib.Path(self.github_manifest).read_bytes(), "application/json")
            return
        self.send_error(http.HTTPStatus.NOT_FOUND)

    def send_json(self, payload):
        self.send_content(json.dumps(payload).encode(), "application/json")

    def send_content(self, content, content_type):
        self.send_response(http.HTTPStatus.OK)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(content)))
        self.end_headers()
        self.wfile.write(content)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--cert", required=True)
    parser.add_argument("--key", required=True)
    parser.add_argument("--directory", required=True)
    parser.add_argument("--port", required=True, type=int)
    parser.add_argument("--github-token", default="")
    parser.add_argument("--github-manifest", default="")
    args = parser.parse_args()

    handler = lambda *values: Handler(
        *values,
        directory=args.directory,
        github_token=args.github_token,
        github_manifest=args.github_manifest,
    )
    server = http.server.ThreadingHTTPServer(("127.0.0.1", args.port), handler)
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(args.cert, args.key)
    server.socket = context.wrap_socket(server.socket, server_side=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
