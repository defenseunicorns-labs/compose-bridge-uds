#!/usr/bin/env python3
import argparse
import json
import os
from http.server import BaseHTTPRequestHandler, HTTPServer

APP_NAME = os.getenv("APP_NAME", "hello-world")
APP_MESSAGE = os.getenv("APP_MESSAGE", "A slightly more glamorous hello")
APP_PORT = int(os.getenv("APP_PORT", "8080"))


class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        return

    def _json(self, code, payload):
        data = json.dumps(payload).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        if self.path in ("/health", "/healthz"):
            self._json(200, {"status": "ok", "app": APP_NAME})
            return

        if self.path != "/":
            self._json(404, {"error": "not found"})
            return

        page = f"""<!doctype html>
<html>
  <head>
    <meta charset=\"utf-8\" />
    <title>{APP_NAME}</title>
    <style>
      body {{
        margin: 0;
        font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
        background: radial-gradient(circle at top right, #2b6cb0, #0f172a);
        color: #f8fafc;
        display: grid;
        min-height: 100vh;
        place-items: center;
      }}
      .card {{
        background: rgba(15, 23, 42, 0.66);
        border: 1px solid rgba(148, 163, 184, 0.35);
        border-radius: 16px;
        box-shadow: 0 20px 70px rgba(15, 23, 42, 0.45);
        max-width: 720px;
        padding: 2rem;
      }}
      h1 {{ margin: 0 0 .5rem 0; font-size: 2rem; }}
      p {{ opacity: .95; line-height: 1.5; }}
      code {{ color: #93c5fd; }}
    </style>
  </head>
  <body>
    <main class=\"card\">
      <h1>{APP_MESSAGE}</h1>
    </main>
  </body>
</html>
"""
        body = page.encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def run_healthcheck():
    print("ok")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, default=APP_PORT)
    parser.add_argument("--healthcheck", action="store_true")
    args = parser.parse_args()

    if args.healthcheck:
        run_healthcheck()
        return

    server = HTTPServer(("0.0.0.0", args.port), Handler)
    print(f"{APP_NAME} listening on :{args.port}")
    server.serve_forever()


if __name__ == "__main__":
    main()
