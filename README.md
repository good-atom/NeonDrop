# NeonDrop

NeonDrop is a local-network clipboard and file transfer application written in Go.
Open one server URL on devices connected to the same Wi-Fi, select an active device,
and send text or files without using a cloud service.

## Features

- Browser-based device registration with persistent local identity.
- Live presence using Server-Sent Events.
- Green neon indicator for online devices and red indicator for offline devices.
- Addressed text and file transfers between selected devices.
- Upload progress, incoming notifications, activity history, and authenticated downloads.
- Temporary local storage with automatic 24-hour cleanup.
- Responsive desktop and mobile interface embedded into the Go binary.
- Directly attached private-subnet enforcement: public, routed, and VPN clients are rejected.
- `HttpOnly` sessions, same-origin enforcement, and restrictive browser security headers.

## Run

Go 1.24 or newer is required.

```bash
make run
```

The server prints URLs such as:

```text
This device: http://localhost:8080
Local network: http://192.168.1.42:8080
```

Open the local-network URL on another device connected to the same Wi-Fi.

To use a different address or storage directory:

```bash
go run ./cmd/neondrop -addr :9090 -data ./data
```

## Test and build

```bash
make test
make build
./bin/neondrop
```

## Architecture

```text
Browser A ── HTTP/SSE ──┐
                        │
Browser B ── HTTP/SSE ──┼── Go LAN hub ── temporary file storage
                        │
Browser C ── HTTP/SSE ──┘
```

The Go server coordinates device presence and transfers. Every server process owns an
independent in-memory device registry, so devices from another NeonDrop server or
another network cannot appear in it. Files travel through the local server rather than
directly between browsers. This keeps the MVP reliable on mobile browsers and avoids
cloud infrastructure.

## Security model

NeonDrop is intended for trusted private networks:

- Requests are accepted only from loopback or a directly attached private IPv4 subnet.
- VPN, tunnel, public, and other routed source addresses are rejected.
- Browser sessions use random tokens stored in `HttpOnly`, `SameSite=Strict` cookies.
- Existing device identities cannot be claimed without their current session.
- Downloads are limited to the sender and intended recipient.
- Mutation requests from foreign browser origins are rejected.
- Filenames are sanitized and uploads are stored under generated IDs.
- Offline devices disappear from discovery after 10 minutes.
- Files, transfers, and stale device sessions expire after 24 hours.

This version does not yet implement PIN pairing or end-to-end encryption. The server
machine temporarily handles plaintext transfers, and users already connected to the
same trusted Wi-Fi can register a new device. Do not expose port `8080` through router
port forwarding, a reverse proxy, or a public tunnel. See [SECURITY.md](SECURITY.md).

## Reference

The product direction is inspired by
[decentpaste/decentpaste](https://github.com/decentpaste/decentpaste), while the
implementation and browser-first LAN hub architecture are independent and written in Go.

## License

[MIT](LICENSE)
