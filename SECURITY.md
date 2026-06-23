# Security

## Network boundary

NeonDrop is designed for a private IPv4 LAN.

The server accepts requests only when the remote address is:

- a loopback address; or
- inside a directly attached private IPv4 subnet.

Point-to-point and common VPN/tunnel interfaces (`utun`, `tun`, `tap`, `wg`, `ppp`,
and `ipsec`) are excluded. A request arriving from the public internet, a VPN subnet,
or another routed network receives HTTP `403`.

Each NeonDrop process has an independent in-memory device registry. Devices connected
to another NeonDrop server cannot appear in this server's device list.

## Session and transfer protection

- Browser sessions use a random 256-bit token in an `HttpOnly`, `SameSite=Strict`
  cookie.
- Session tokens are not stored in JavaScript or placed in EventSource URLs.
- An existing device ID cannot be registered again without its current session.
- Transfer lists are filtered by authenticated device ID.
- Files can be downloaded only by their sender or intended recipient.
- Filenames are sanitized and files are stored under random server-generated IDs.
- Cross-origin mutation requests are rejected.
- Browser content is protected by a restrictive Content Security Policy.
- Offline devices disappear from discovery after 10 minutes.
- Sessions, device metadata, messages, and files expire after 24 hours.
- Temporary files are removed when the server shuts down normally.

## Important limitation

NeonDrop currently protects traffic from other networks and isolates transfers by
device session. It does not yet provide end-to-end encryption or PIN-based pairing.
The machine running NeonDrop can access temporary plaintext transfers, and any person
already connected to the same trusted Wi-Fi can open the application and register a
new device.

Do not expose port `8080` through router port forwarding, a public reverse proxy, or a
cloud tunnel. Do not use NeonDrop on an untrusted public Wi-Fi network.

## Reporting a vulnerability

Do not publish credentials, private files, or exploit details in a public issue.
Open a GitHub security advisory for the repository instead.
