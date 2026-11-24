"""Loopback RFB 3.8 read-only proof, no WebSocket/auth/control management.

Only pixel format, encodings and framebuffer requests may reach Xvnc. Input,
clipboard and unknown extensions close the connection on the server side.
"""

import socket
import socketserver
import struct
import sys
import threading


def read(stream, size):
    data = bytearray()
    while len(data) < size:
        part = stream.recv(size - len(data))
        if not part:
            raise EOFError()
        data.extend(part)
    return bytes(data)


class Viewer(socketserver.BaseRequestHandler):
    def handle(self):
        client = self.request
        client.settimeout(15)
        with socket.create_connection(('127.0.0.1', self.server.upstream), timeout=15) as upstream:
            try:
                assert read(upstream, 12) == b'RFB 003.008\n'
                client.sendall(b'RFB 003.008\n')
                assert read(client, 12) == b'RFB 003.008\n'
                upstream.sendall(b'RFB 003.008\n')
                options = read(upstream, read(upstream, 1)[0])
                assert 1 in options
                client.sendall(b'\x01\x01')
                assert read(client, 1) == b'\x01'
                upstream.sendall(b'\x01')
                assert read(upstream, 4) == b'\0\0\0\0'
                client.sendall(b'\0\0\0\0')
                read(client, 1)
                upstream.sendall(b'\x01')
                header = read(upstream, 24)
                client.sendall(header + read(upstream, struct.unpack('>I', header[20:24])[0]))

                def frames():
                    try:
                        while True:
                            data = upstream.recv(65536)
                            if not data:
                                break
                            client.sendall(data)
                    except OSError:
                        pass
                    finally:
                        try:
                            client.shutdown(socket.SHUT_RDWR)
                        except OSError:
                            pass

                thread = threading.Thread(target=frames, daemon=True)
                thread.start()
                while True:
                    kind = read(client, 1)
                    if kind == b'\x00':
                        body = read(client, 19)
                    elif kind == b'\x02':
                        body = read(client, 3)
                        count = struct.unpack('>H', body[1:])[0]
                        if count > 256:
                            break
                        body += read(client, 4 * count)
                    elif kind == b'\x03':
                        body = read(client, 9)
                    else:
                        print('rejected RFB client message ' + str(kind[0]), flush=True)
                        break
                    upstream.sendall(kind + body)
            except (EOFError, OSError, AssertionError):
                pass
            finally:
                try:
                    upstream.shutdown(socket.SHUT_RDWR)
                    client.shutdown(socket.SHUT_RDWR)
                except OSError:
                    pass


if __name__ == '__main__':
    class Server(socketserver.ThreadingTCPServer):
        allow_reuse_address = True
        daemon_threads = True
    with Server(('127.0.0.1', int(sys.argv[1])), Viewer) as server:
        server.upstream = int(sys.argv[2])
        server.serve_forever()
