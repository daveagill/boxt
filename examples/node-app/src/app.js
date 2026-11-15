import http from "node:http";

export function handler(req, res) {
  if (req.url === "/health") {
    res.writeHead(200, { "content-type": "application/json" });
    res.end(JSON.stringify({ ok: true }));
    return;
  }
  res.writeHead(200, { "content-type": "text/plain; charset=utf-8" });
  res.end("Hello from boxt\n");
}

export function createServer() {
  return http.createServer(handler);
}
