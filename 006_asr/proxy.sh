#!/bin/bash
# Tiny HTTP/HTTPS CONNECT proxy to bypass Colima network issues.
# Run on HOST (macOS), Docker containers connect via host.docker.internal:8889
set -euo pipefail
python3 -c '
import socket, threading, select, sys

def handle(client):
    server = None
    try:
        data = client.recv(4096)
        if not data: return
        line = data.split(b"\n")[0].decode()
        parts = line.split()
        if len(parts) < 2: return
        method, path = parts[0], parts[1]

        if method == "CONNECT":
            host, port = path.split(":")
            server = socket.create_connection((host, int(port)), timeout=15)
            client.sendall(b"HTTP/1.1 200 Connection Established\r\n\r\n")
        elif path.startswith("http://"):
            host = path.split("://")[1].split("/")[0]
            server = socket.create_connection((host, 80), timeout=15)
            server.sendall(data)
        else:
            return

        # Bidirectional relay
        sockets = [client, server]
        while True:
            r, _, _ = select.select(sockets, [], [], 30)
            if not r: break
            for s in r:
                try:
                    d = s.recv(32768)
                    if not d: return
                    other = sockets[1] if s is sockets[0] else sockets[0]
                    other.sendall(d)
                except:
                    return
    except Exception as e:
        print(f"proxy error: {e}", file=sys.stderr)
    finally:
        client.close()
        if server:
            server.close()

s = socket.socket()
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("0.0.0.0", 8889))
s.listen(50)
print("=== yt2srt proxy: listening on :8889 ===", flush=True)
while True:
    client, addr = s.accept()
    threading.Thread(target=handle, args=(client,), daemon=True).start()
'
