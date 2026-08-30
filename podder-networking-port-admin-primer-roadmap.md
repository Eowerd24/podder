# Podder Networking & Port Administration
## Implementation Primer, Architecture Handoff, and Roadmap

**Target repository:** `https://github.com/Eowerd24/podder`  
**Repository:** `Eowerd24/podder`  
**Prepared:** 2026-08-30  
**Primary implementation environment:** Linux + rootless Podman  
**UI stack:** Wails v3 + Go backend + vanilla HTML/CSS/JavaScript frontend  
**Purpose of this document:** Give ChatGPT Work enough architectural, product, safety, and codebase context to implement a substantial networking/port-management evolution of Podder without requiring the original design conversation.

---

# 0. Instructions to ChatGPT Work

Treat this document as the product/architecture handoff for the next major Podder feature family.

Your job is to **inspect the actual current repository, preserve existing behavior, implement the roadmap incrementally, test each stage, and avoid unsafe shortcuts around container recreation**.

Do not assume this document is a substitute for reading the repository. Start by inspecting the current `main` branch and verifying that the described code still matches reality.

Work in small, reviewable changes. Prefer a sequence of coherent commits or work packages over one giant rewrite.

The first deliverable should **not** be "click Edit Port and mutate arbitrary containers." The first deliverable should make Podder a trustworthy observer of container networking and host port use. Port-changing capability comes only after discovery, conflict detection, provenance/ownership, and recreation safety exist.

If code reality conflicts with this handoff, preserve the repository's current behavior and document the discrepancy before choosing a design.

Do not hard-code this owner's homelab hostnames, IPs, paths, or registry location into the general Podder application. Homelab integration should be an optional configurable capability.

---

# 1. Mission

Evolve Podder from a lightweight Podman GUI/compose helper into a **lightweight, safety-aware local Podman control plane with first-class networking and port administration**.

The target experience is:

> Podder can show what ports containers publish, what host listeners already exist, what endpoints are declared in an optional external registry, whether a proposed mapping is safe, how a container is managed, and—when Podder has an authoritative deployment definition—change the mapping through a controlled recreate/verify/rollback workflow.

The feature should remain understandable to a single homelab operator and useful to ordinary Podman users.

The desired product identity is **not** "a giant Kubernetes replacement." It is:

> **Podder: a small native Podman control center that combines runtime observation, declarative local workload knowledge, and safe changes.**

---

# 2. Design Priorities

Use these priorities when choosing between competing approaches:

1. **Simplicity**
2. **Reliability**
3. **Safety / reversibility**
4. **Clear ownership of configuration**
5. **Low resource overhead**
6. **Good local UX**
7. **Extensibility**
8. **Performance**

A clever abstraction that makes common operations harder is a regression.

A UI button that works for 80% of containers but can silently recreate the other 20% incorrectly is unacceptable.

---

# 3. Repository-Grounded Baseline

This section summarizes the current repository state that motivated the roadmap. Verify it against the repository before editing.

## 3.1 Current product

Podder describes itself as a lightweight Podman GUI control panel and CLI compose helper.

Current major capabilities include:

- Dashboard with host/container statistics.
- Container list.
- Start container.
- Stop container.
- Restart container.
- Remove container.
- View recent/streaming logs.
- List images.
- Pull images.
- Run an image as a container.
- Bind-mount support when running containers.
- Build images.
- `pod up` / `pod down` compose helper behavior.
- Native Podman command passthrough for other CLI arguments.
- Rootless Podman socket preflight for compose support.

The current release metadata in the repository identifies `1.1.2` as the release target.

## 3.2 Current code structure

Important files currently include:

```text
main.go
main_test.go
podman.go
podman_test.go

frontend/
  index.html
  package.json
  src/
    main.js
  public/
    style.css

README.md
ABOUT.md
CHANGELOG.md
Taskfile.yml
go.mod
go.sum
```

The Go module is currently named:

```text
module changeme
```

Do **not** mix a Go module rename into the networking feature unless the owner explicitly wants that cleanup. It is unrelated technical debt.

## 3.3 Backend shape

`PodmanService` in `podman.go` is the main Wails-bound backend service.

The backend executes the local `podman` CLI using `exec.Command`, parses JSON responses, and exposes methods to the frontend.

The current `Container` struct includes fields such as:

- `Id`
- `Names`
- `Image`
- `ImageID`
- `State`
- `Status`
- `Created`
- `ExitCode`
- `Command`
- `AutoRemove`

It currently does **not** model published port mappings as a first-class field.

The current `ListContainers()` flow uses:

```text
podman ps --format json
```

with `-a` when requested.

## 3.4 Existing port support is creation-only

`RunContainer(...)` currently accepts a single string called `ports`.

`buildRunContainerArgs(...)` adds:

```text
-p <ports>
```

when that value is non-empty.

This means Podder already has the beginning of port publication support, but it is only an input to container creation and is not modeled as structured state.

Important limitations of the current design:

- Port mappings are entered as an opaque string.
- The UI does not expose structured bind IP / host port / container port / protocol fields.
- Existing mappings are not shown on container cards.
- Multiple mappings are not properly modeled as a collection.
- Existing containers do not have an "Edit Port Mapping" operation.
- Port conflicts are not checked before creation.
- Host-native listeners outside Podman are not considered.
- Podder does not know whether a container came from Compose, Quadlet, a Pod, Podder itself, or an ad-hoc `podman run`.

## 3.5 CLI behavior

`main.go` currently keeps CLI behavior deliberately small:

```text
podder
    -> GUI

podder up
podder down
    -> compose provider logic

podder help
    -> Podder help + native Podman help behavior

podder <anything else>
    -> native podman passthrough
```

Preserve this simplicity.

Networking work does not initially need a large CLI command framework.

A future `pod ports` command may be useful, but it should not block the GUI/backend work.

## 3.6 Frontend shape

The GUI currently has three top-level tabs:

```text
Dashboard
Containers
Images
```

`frontend/src/main.js` uses a hard-coded tab index map similar to:

```js
const tabIndexMap = {
  dashboard: 0,
  containers: 1,
  images: 2
};
```

Adding a Ports tab therefore requires coordinated changes in:

- `frontend/index.html`
- `frontend/src/main.js`
- likely `frontend/public/style.css`

The frontend is intentionally lightweight vanilla JavaScript. Do not introduce React/Vue/etc. just for this feature.

## 3.7 Current tests

There are existing Go tests covering:

- container JSON parsing
- image JSON parsing
- system info parsing
- run-argument construction
- bind mount argument validation
- image file validation
- compose provider choice
- Podman socket path logic
- Podman socket startup/error handling

Preserve these tests.

New networking behavior should add substantial pure-function/unit-test coverage.

---

# 4. Podman Facts the Design Must Respect

The current Podman documentation should be treated as authoritative when implementing the runtime layer.

Important relevant facts:

## 4.1 Published port form

Podman uses forms equivalent to:

```text
[[hostIP:][hostPort]:]containerPort[/protocol]
```

Examples:

```text
8080:80
127.0.0.1:8080:80
127.0.0.1:8080:80/tcp
0.0.0.0:5353:5353/udp
```

## 4.2 Bind address matters

A mapping is **not** adequately represented by host port alone.

These are materially different:

```text
127.0.0.1:3000
192.168.0.15:3000
0.0.0.0:3000
[::]:3000
```

A move from loopback to wildcard is an exposure/security change and must be highlighted as such.

## 4.3 Protocol matters

These are separate claims:

```text
3000/tcp
3000/udp
```

Never key conflicts by integer port alone.

## 4.4 Runtime inspection

Podman exposes port mapping information through `podman port` and container inspection data.

Use structured JSON where possible.

Do not scrape human-formatted `podman ps` output if a JSON source exists.

## 4.5 Port publication is creation/deployment configuration

Do not design the UX as if changing a published port is simply an in-place property mutation on an arbitrary running container.

The safe mental model is:

```text
inspect authoritative configuration
        ->
validate proposed configuration
        ->
recreate/redeploy through the correct owner
        ->
verify
        ->
commit new known-good state
```

For Pods, publication belongs to the Pod rather than to individual containers sharing the Pod network namespace.

## 4.6 Quadlet

Podman Quadlet supports declarative `PublishPort=` definitions.

Example:

```ini
[Container]
Image=nginx:alpine
PublishPort=127.0.0.1:8080:80
```

This is a strong long-term target for durable single-host Podman services.

Official docs worth consulting during implementation:

- https://docs.podman.io/en/latest/markdown/podman-inspect.1.html
- https://docs.podman.io/en/latest/markdown/podman-create.1.html
- https://docs.podman.io/en/latest/markdown/podman-systemd.unit.5.html
- https://docs.podman.io/en/latest/markdown/podman-quadlet-basic-usage.7.html

Verify current documentation during implementation rather than freezing behavior to this primer.

---

# 5. The External Homelab Port Registry

The owner has started a Git-managed homelab-wide port registry named `ports.yaml`.

It is meant to represent **declared/intended state**, while runtime tools such as Podder represent **observed state**.

This distinction is foundational:

```text
Git registry = what SHOULD exist
Runtime      = what DOES exist
```

Podder should eventually reconcile those two states.

## 5.1 Registry concepts

The registry currently models concepts such as:

- endpoint ID
- service
- node
- provider
- container ID
- protocol
- application protocol
- listener address
- listener port
- container port
- scope
- class
- lifecycle state
- verification state
- purpose
- notes
- DNS identity
- resource metadata

Lifecycle vocabulary currently includes:

```text
active
reserved
planned
temporary
deprecated
retired
```

Scope vocabulary currently includes:

```text
loopback
guest
management
lan
overlay
cluster
public
```

Verification vocabulary currently includes:

```text
confirmed
platform-default
```

## 5.2 Important: current supplied registry copy is malformed

The currently supplied `ports.yaml`/`ports.md` copy contains copy/paste/YAML errors.

Examples seen in the supplied file include:

```yaml
- - id: rig9-open-webui
```

instead of:

```yaml
- id: rig9-open-webui
```

and:

```yaml
d: files1-smb
```

instead of:

```yaml
- id: files1-smb
```

Several records also appear to have escaped the indentation level beneath:

```yaml
ports:
```

Therefore:

> **Do not build a parser that accepts the malformed document as canonical syntax.**

First create/validate a clean V1 schema fixture.

The actual registry should be corrected independently and checked with a YAML parser.

## 5.3 Registry integration must be optional

Podder is a public/general project.

Do not hard-code:

```text
homelab-foundation
inventory/network/ports.yaml
rig9
pve7
rack1
```

Instead support something such as:

```text
Settings
  Port Registry
    [ ] Enable external registry
    Path: /path/to/ports.yaml
```

or a configuration property/environment variable.

Podder must remain fully useful when no registry exists.

## 5.4 Registry should initially be read-only

Do not immediately let Podder rewrite the registry.

Reasons:

- YAML comments are valuable human documentation.
- naive marshal/unmarshal may reorder or discard formatting/comments.
- the registry is Git-authoritative.
- accidental destructive rewrites are worse than requiring manual edits.
- schema evolution is still early.

Initial integration:

```text
read
validate
display
reconcile
```

Later integration may add carefully controlled writes.

---

# 6. Known Example Endpoints from the Owner's Current Environment

These exist to provide realistic fixtures and UI examples. They are **not** defaults for Podder.

```text
rig9
  127.0.0.1:3000/tcp   Open WebUI
  127.0.0.1:3100/tcp   Flowise host mapping
  127.0.0.1:5678/tcp   n8n
  127.0.0.1:8888/tcp   Unsloth web/chat
  127.0.0.1:11435/tcp  llama.cpp
  TCP/2049              NFS model export (LAN)

files1
  192.168.0.18:445/tcp   Samba
  192.168.0.18:3923/tcp  Copyparty HTTPS

pve7
  192.168.0.19:22/tcp    SSH
  192.168.0.19:8006/tcp  Proxmox

rack1
  192.168.0.29:22/tcp    SSH
  192.168.0.29:8006/tcp  Proxmox

rack1 BMC
  192.168.0.30:443/tcp
  192.168.0.30:623/udp

pve-lab
  192.168.0.8:22/tcp
  192.168.0.8:8006/tcp

witness1
  TCP/2514 reserved for RELP

wikijs1
  192.168.0.28:3000/tcp

code-memory
  human viewer observed at 127.0.0.1:9749/tcp
```

Do not infer management authority from these examples.

Also note that a loopback address associated with a VM/LXC provider can be semantically ambiguous: `127.0.0.1` is local to whichever network namespace actually owns the listener. Registry reconciliation must not silently assume that every `127.0.0.1` means the Podder desktop host.

---

# 7. Core Domain Model

The networking feature should normalize everything into a small set of concepts.

## 7.1 Network endpoint identity

At minimum, a local endpoint claim is:

```text
address family
bind address
host port
transport protocol
```

For container publication, additionally:

```text
container port
```

Suggested Go model:

```go
type PortMapping struct {
    HostIP        string `json:"hostIP"`
    HostPort      uint16 `json:"hostPort"`
    ContainerPort uint16 `json:"containerPort"`
    Protocol      string `json:"protocol"`
}
```

Consider retaining whether HostIP was explicitly specified.

For display, normalize an unspecified bind carefully. Do not silently rewrite it to `0.0.0.0` if Podman's actual semantics include IPv6/all-interface behavior.

## 7.2 Host listener

```go
type HostListener struct {
    Address   string `json:"address"`
    Port      uint16 `json:"port"`
    Protocol  string `json:"protocol"`
    Process   string `json:"process,omitempty"`
    PID       int    `json:"pid,omitempty"`
    Source    string `json:"source"`
}
```

Process/PID may be unavailable to an unprivileged/rootless desktop process. That is acceptable.

The listener still matters even if ownership is unknown.

## 7.3 Port claim

Use a normalized internal claim for conflict checking:

```go
type PortClaim struct {
    Address  string
    Port     uint16
    Protocol string
    Source   string
    OwnerID  string
}
```

Sources might include:

```text
podman
host-listener
registry-active
registry-reserved
registry-planned
```

## 7.4 Management provenance

The application needs to distinguish who owns deployment configuration.

Suggested enum:

```text
podder
compose
quadlet
pod
ad-hoc
unknown
```

Potential structure:

```go
type ManagementOrigin struct {
    Type       string `json:"type"`
    ConfigPath string `json:"configPath,omitempty"`
    Service    string `json:"service,omitempty"`
    Unit       string `json:"unit,omitempty"`
    PodName    string `json:"podName,omitempty"`
    Confidence string `json:"confidence"`
}
```

Do not label something "Compose-managed" merely because its labels vaguely resemble Compose without preserving confidence/evidence.

## 7.5 Reconciliation state

Suggested statuses:

```text
match
undeclared
declared-missing
mismatch
reserved-free
reserved-in-use
planned
unresolved
```

Do not overload "conflict" to also mean "drift." They are related but different concepts.

---

# 8. Product Boundary

Podder should distinguish two categories.

## 8.1 Container endpoints

Podder may eventually administer these when configuration ownership is known.

Examples:

```text
127.0.0.1:3100 -> 3000/tcp
127.0.0.1:3000 -> 8080/tcp
```

## 8.2 Non-container host/infrastructure endpoints

Podder may observe and display them, but should not automatically reconfigure them.

Examples:

```text
SSH
Proxmox 8006
NFS
BMC
DNS
systemd-native daemons
RELP collectors
```

Therefore:

```text
Port Registry
    |
    +-- container endpoints -> Podder may eventually apply
    |
    +-- host/infrastructure endpoints -> read-only/observed in Podder
```

This prevents Podder from growing into an uncontrolled general-purpose network configuration editor.

---

# 9. Safety Invariants

These should be treated as product rules, not suggestions.

## 9.1 Never silently widen exposure

Changing:

```text
127.0.0.1:3100
```

to:

```text
0.0.0.0:3100
```

must trigger a high-visibility warning.

Likewise:

```text
127.0.0.1
->
specific LAN IP
```

is an exposure change.

The UI should describe the consequence in plain language.

## 9.2 Never reconstruct unknown containers optimistically

Do not implement:

```text
inspect container
guess podman run arguments
delete original
create replacement
hope
```

for arbitrary ad-hoc containers.

Container configuration may include:

- volumes
- bind mounts
- environment variables
- secrets
- command/entrypoint
- user
- workdir
- capabilities
- devices
- GPUs
- security options
- labels
- networks
- DNS
- restart policy
- health checks
- namespaces
- resource limits
- tmpfs
- ulimits
- SELinux options
- systemd integration
- pods

A port change is not worth losing these.

## 9.3 Respect management authority

If a container is Compose-managed, the Compose definition should be the source of deployment configuration.

If Quadlet-managed, the Quadlet should be the source.

If Pod-owned, the Pod port publication must be considered.

If Podder-managed, Podder's own spec may be authoritative.

Unknown/ad-hoc containers should remain read-only until explicitly adopted.

## 9.4 Validate before destructive action

For any future mapping change:

```text
parse
validate syntax
validate requested port range
check conflict
check ownership
capture rollback state
only then modify/redeploy
```

## 9.5 Verify after deployment

Success means more than command exit code 0.

At minimum verify:

- expected container exists
- expected running state, when it was previously running
- expected port mapping is present
- old mapping is gone if it was supposed to be removed

Later add optional health/readiness verification.

## 9.6 Rollback is part of the change design

If Podder controls the authoritative spec, retain the previous spec until verification succeeds.

If apply fails:

```text
restore previous spec
redeploy previous known-good state
report failure and rollback result
```

Never erase the last known-good configuration first.

---

# 10. Phase 1 Product: Networking Observation

This should be the first major implementation milestone.

## 10.1 Container card enhancement

Every container card should display published mappings.

Example:

```text
Open WebUI

running

Image
ghcr.io/open-webui/open-webui

Ports
127.0.0.1:3000 -> 8080/tcp

Status
...
```

Multiple mappings should render as multiple rows/badges.

Containers with no published mappings should show:

```text
Ports
None
```

Do not confuse image `EXPOSE` metadata with actually published host ports.

## 10.2 New Ports tab

Add:

```text
Dashboard
Containers
Images
Ports
```

Suggested initial layout:

```text
Ports

[Refresh] [Filter...]

Observed container mappings
-----------------------------------------------------
Container     Bind          Host    Target    Proto
open-webui    127.0.0.1     3000    8080      TCP
flowise       127.0.0.1     3100    3000      TCP

Host listeners not owned by visible Podman mappings
-----------------------------------------------------
Bind          Port    Proto   Process
127.0.0.1     5678    TCP     ...
0.0.0.0       22      TCP     ...

Optional registry status
-----------------------------------------------------
...
```

## 10.3 Copy/open helpers

Useful low-risk actions:

```text
Copy endpoint
Copy URL
Open endpoint
```

Only offer "Open" when application protocol is known or a reasonable explicit mapping exists.

Do not assume every TCP listener speaks HTTP.

---

# 11. Runtime Port Discovery

Use multiple evidence sources and normalize them.

## 11.1 Podman mappings

Preferred evidence should come from structured Podman inspection and/or `podman port`.

A robust implementation can:

1. list containers
2. inspect relevant containers
3. parse published port bindings
4. normalize into `[]PortMapping`

Avoid running an expensive subprocess per container every 5 seconds if one structured query can provide the same data.

Measure before optimizing.

## 11.2 Host listeners

On Linux, use `ss` as the initial observer.

A reasonable V1 command family is based on:

```text
ss -H -lnt
ss -H -lnu
```

or an equivalent combined invocation.

Adding process info can be attempted, but do not make it mandatory because rootless users may not see ownership information for every listener.

Do not shell-interpolate user input.

Use `exec.Command` arguments.

## 11.3 Observation provenance

Every observed record should retain its source.

Example:

```json
{
  "source": "podman",
  "containerId": "...",
  "hostIP": "127.0.0.1",
  "hostPort": 3100,
  "containerPort": 3000,
  "protocol": "tcp"
}
```

versus:

```json
{
  "source": "host-listener",
  "address": "0.0.0.0",
  "port": 22,
  "protocol": "tcp"
}
```

This lets the UI explain what it knows instead of pretending all discovery has equal certainty.

---

# 12. Port Conflict Engine

Implement this as pure Go logic with exhaustive tests.

It should be usable by:

- Run Container
- future Edit Port Mapping
- free-port suggestion
- registry reconciliation
- future CLI checks

## 12.1 Conflict key

At minimum:

```text
address
port
protocol
address-family semantics
```

## 12.2 Required behavior

A proposed:

```text
127.0.0.1:3000/tcp
```

conflicts with:

```text
127.0.0.1:3000/tcp
0.0.0.0:3000/tcp
```

A proposed:

```text
192.168.0.15:3000/tcp
```

conflicts with:

```text
192.168.0.15:3000/tcp
0.0.0.0:3000/tcp
```

A proposed wildcard:

```text
0.0.0.0:3000/tcp
```

should conservatively conflict with any IPv4 bind on TCP/3000.

TCP and UDP should not conflict merely because the integer port is the same.

IPv6 wildcard/dual-stack behavior can vary by OS/socket configuration. For V1, prefer conservative "potential conflict" classification over a false safe result.

## 12.3 Conflict classes

Suggested:

```text
hard-conflict
reserved
potential-conflict
free
unknown
```

Example display:

```text
Host port 5678/tcp

HARD CONFLICT
127.0.0.1:5678 is currently listening.

Observed owner:
n8n / host listener
```

Registry reservation:

```text
Host port 2514/tcp

RESERVED
External registry reserves this port for witness1-relp.
```

## 12.4 Free-port suggestions

A future helper:

```text
[ Find free port ]
```

should check:

1. Podman mappings
2. host listeners
3. active external registry claims
4. reserved external registry claims

Do not rely only on attempting a temporary `bind()`.

Suggested candidate statuses:

```text
3110  FREE
3111  FREE
5678  ACTIVE
8888  ACTIVE
2514  RESERVED
```

Do not automatically choose privileged ports for rootless containers.

---

# 13. External Registry Integration

Implement after local observation is reliable.

## 13.1 Configuration

Support an optional registry file path.

Possible implementation:

```text
~/.config/podder/config.json
```

or another XDG-appropriate configuration location.

Example:

```json
{
  "portRegistry": {
    "enabled": true,
    "path": "/srv/repos/homelab-foundation/inventory/network/ports.yaml"
  }
}
```

Do not hard-code that example path.

## 13.2 YAML dependency

The current project does not have a YAML parser dependency.

Adding a small maintained YAML library is reasonable.

A likely choice is:

```text
gopkg.in/yaml.v3
```

Confirm current maintenance/security posture before adding it.

## 13.3 Parse only what Podder needs

Do not bind the application tightly to every registry field.

Suggested tolerant model:

```go
type PortRegistry struct {
    Version int            `yaml:"version"`
    Ports   []RegistryPort `yaml:"ports"`
}

type RegistryPort struct {
    ID                  string            `yaml:"id"`
    Service             string            `yaml:"service"`
    Node                string            `yaml:"node"`
    Provider            string            `yaml:"provider"`
    ContainerID         int               `yaml:"container_id"`
    Protocol            string            `yaml:"protocol"`
    ApplicationProtocol string            `yaml:"application_protocol"`
    Listener            RegistryListener  `yaml:"listener"`
    Container           RegistryContainer `yaml:"container"`
    Scope               string            `yaml:"scope"`
    State               string            `yaml:"state"`
    Verification        string            `yaml:"verification"`
    Purpose             string            `yaml:"purpose"`
}
```

Unknown YAML keys should not fail parsing unless strict schema mode is explicitly selected.

## 13.4 Invalid registry behavior

The registry is optional.

If malformed:

```text
External port registry: INVALID
Line 73: ...
```

Podder should still operate normally for local Podman/host discovery.

Never make a malformed optional homelab file prevent the application from managing containers.

## 13.5 Reconciliation

Initial matching should be conservative.

Good matching evidence:

- explicit container ID
- explicit provider/container association
- exact address+port+protocol
- known service label mapped to container metadata

Avoid name guessing that could associate the wrong declared service with a container.

UI statuses can be:

```text
MATCH
UNDECLARED
DECLARED / NOT OBSERVED
MISMATCH
RESERVED
```

---

# 14. Management-Origin Detection

This is required before safe port changes.

## 14.1 Why it matters

A container can be launched by:

- Podder
- `podman run`
- Compose
- Quadlet/systemd
- a Pod
- another tool

The correct port-change operation depends on that origin.

## 14.2 Detection evidence

Inspect:

- Podman labels
- container annotations/metadata
- Pod membership
- systemd/Quadlet relationships
- known Podder labels
- stored Podder specs

Do not rely solely on container names.

## 14.3 Podder labels

For newly Podder-managed workloads, add explicit labels.

Example concept:

```text
io.podder.managed=true
io.podder.spec=<stable-spec-id>
io.podder.version=<version>
```

Choose final names carefully and document them.

This makes future management unambiguous.

## 14.4 UI badges

Container cards/details should eventually show:

```text
Managed by Podder
Managed by Compose
Managed by Quadlet
Member of Pod <name>
Ad-hoc
Unknown
```

The "Edit Port Mapping" action should be disabled or adapted based on this status.

---

# 15. Podder-Managed Specs

This is the safest foundation for editable ad-hoc containers.

## 15.1 Goal

When Podder creates a durable managed container, persist enough declarative configuration to recreate it exactly.

Example location:

```text
~/.config/podder/services/
```

Use XDG conventions rather than a hard-coded home path.

Example:

```text
~/.config/podder/services/open-webui.yaml
```

or JSON if avoiding another serialization layer for Podder's internal config.

## 15.2 Example conceptual spec

```yaml
apiVersion: podder/v1
kind: Container

metadata:
  name: flowise

spec:
  image: flowiseai/flowise:latest

  ports:
    - hostIP: 127.0.0.1
      hostPort: 3100
      containerPort: 3000
      protocol: tcp

  mounts:
    - type: bind
      source: /home/user/containers/flowise
      target: /root/.flowise

  restartPolicy: unless-stopped
```

The real schema must eventually cover everything Podder allows users to configure.

Do not promise exact recreation if the spec does not capture a setting.

## 15.3 Atomic persistence

Write specs atomically:

```text
write temp file
fsync as appropriate
rename
```

Keep previous known-good spec until deployment verification completes.

---

# 16. Adopt Into Podder

This is a later feature and potentially one of Podder's most distinctive capabilities.

## 16.1 UX

For an unmanaged container:

```text
This container has no Podder-owned deployment definition.

Podder can inspect it, but safe recreation is not guaranteed.

[ Adopt into Podder ]
```

## 16.2 Adoption workflow

1. Inspect container thoroughly.
2. Generate a candidate Podder spec.
3. Display every captured area.
4. Highlight unsupported/unrepresentable settings.
5. Refuse adoption if critical configuration cannot be preserved.
6. Require explicit confirmation.
7. Save spec.
8. Add Podder management metadata only after successful validation.

Do **not** recreate the container during initial adoption unless necessary and explicitly confirmed.

## 16.3 Completeness report

Example:

```text
Adoption readiness

Captured
  ✓ image
  ✓ command
  ✓ 2 port mappings
  ✓ environment
  ✓ 3 bind mounts
  ✓ restart policy
  ✓ network

Needs review
  ! device mappings

Unsupported
  ✗ custom security option XYZ

Adoption blocked until unsupported configuration is resolved.
```

This is preferable to quietly dropping settings.

---

# 17. Editing Port Mappings

Do this only after observation, conflicts, ownership, and a recreation source exist.

## 17.1 UX terminology

Use:

```text
Edit Port Mapping
```

not merely:

```text
Change Port
```

The real tuple is:

```text
host bind IP : host port -> container port / protocol
```

## 17.2 Dialog

Example:

```text
Edit Port Mapping

Container
flowise

Managed by
Podder

Bind address
[ 127.0.0.1 ]

Host port
[ 3100 ]

Container port
[ 3000 ]

Protocol
[ TCP ]

Current
127.0.0.1:3100 -> 3000/tcp

Proposed
127.0.0.1:3110 -> 3000/tcp

Validation
✓ syntax valid
✓ 3110/tcp free
✓ no external registry reservation
✓ exposure unchanged

[ Cancel ] [ Apply ]
```

## 17.3 Exposure warning

Example:

```text
EXPOSURE CHANGE

Current:
127.0.0.1:3100

Proposed:
0.0.0.0:3100

The service would change from local-only access to listening
on all IPv4 interfaces reachable according to host firewall/network rules.

[ Cancel ] [ Continue ]
```

This warning must not be a subtle toast.

## 17.4 Podder-managed apply flow

Conceptual transaction:

```text
1. Read current authoritative spec
2. Validate proposed mapping
3. Save candidate spec separately
4. Capture current runtime state
5. Stop old container if required
6. Recreate from candidate spec
7. Restore prior running/stopped intention
8. Verify resulting mapping
9. Optional health check
10. Promote candidate spec to current
11. Retain audit event
```

Failure:

```text
1. report failed step
2. attempt old-spec restoration
3. verify rollback
4. retain both failure + rollback result
```

## 17.5 Never delete persistent data

A container recreation must not remove:

- named volumes
- bind-mounted data
- externally stored configuration

unless the user explicitly asked to remove them.

Avoid destructive flags.

---

# 18. Compose-Managed Containers

Compose should become a first-class management origin, but editing Compose is a separate phase.

## 18.1 Existing Podder behavior

Podder already supports compose up/down and chooses among compose providers.

This is useful infrastructure but is not yet a persistent model of Compose projects.

## 18.2 Future project registry

Podder can eventually remember:

```text
Project name
Compose file path
Working directory
Provider
Services
```

Example:

```json
{
  "name": "flowise",
  "composeFile": "/home/user/stacks/flowise/compose.yml",
  "service": "flowise"
}
```

## 18.3 Port editing

For Compose-managed containers, the authoritative edit is the Compose definition.

Do not directly recreate an individual container behind Compose's back.

Long-term workflow:

```text
parse Compose
edit selected service's ports
write safely
show diff
compose up -d
verify
```

## 18.4 Preserve YAML

Editing arbitrary Compose YAML while preserving anchors, comments, extension fields, interpolation, and formatting is non-trivial.

Do not add Compose file mutation casually.

An intermediate safe feature is:

```text
Managed by Compose
Config: /path/compose.yml

[ Open configuration location ]
[ Copy suggested ports block ]
```

before automated write support.

---

# 19. Quadlet-Managed Containers

Quadlet should eventually be a first-class durable-service path.

## 19.1 Why it fits Podder

Quadlet provides:

- declarative local container units
- systemd lifecycle
- restart/boot semantics
- explicit `PublishPort=`
- simple single-host deployment
- good alignment with rootless Podman

## 19.2 Discovery

Detect relevant `.container` / `.pod` relationships when possible.

Potential rootless location:

```text
~/.config/containers/systemd/
```

Do not assume it is the only possible search path; consult current Podman documentation.

## 19.3 Port edit workflow

For a known `.container` file:

```ini
PublishPort=127.0.0.1:3100:3000
```

change to:

```ini
PublishPort=127.0.0.1:3110:3000
```

then use the appropriate current systemd/Quadlet reload/restart flow and verify.

Preserve comments and unrelated unit content.

Do not implement this until the parser/editing strategy is safe.

---

# 20. Pods

Pods require separate handling.

If a container belongs to a Pod:

- show Pod membership
- show published ports at the Pod level
- do not pretend each member owns the host mapping
- disable individual-container port editing when the Pod owns publication

UI example:

```text
Container: app
Pod: web-stack

Ports
Inherited from Pod:
127.0.0.1:8080 -> 80/tcp

[ Manage Pod Networking ]
```

A later Pod networking view can be added.

---

# 21. UI Architecture

Keep the existing visual style and frontend technology.

## 21.1 Ports tab sections

Recommended V1:

```text
PORTS

Summary:
  Published mappings: 5
  Other host listeners: 14
  Conflicts: 0
  Registry drift: 2

[ All ] [ Podman ] [ Host ] [ Registry ] [ Conflicts ]

Table/list...
```

## 21.2 Row model

Potential columns:

```text
Owner
Source
Bind
Host Port
Target
Protocol
Scope/Exposure
Status
Actions
```

On narrow windows, use cards rather than forcing a wide table.

## 21.3 Exposure badges

Suggested semantic labels:

```text
LOOPBACK
SPECIFIC HOST IP
WILDCARD
UNKNOWN
```

If registry data exists:

```text
LAN
MANAGEMENT
CLUSTER
PUBLIC
```

Do not conflate observed bind address with declared policy scope.

## 21.4 Status badges

Examples:

```text
ACTIVE
FREE
RESERVED
UNDECLARED
MISSING
MISMATCH
POTENTIAL CONFLICT
```

## 21.5 Refresh

Do not aggressively rerun host scans every second.

Good initial behavior:

- manual Refresh button
- refresh on tab entry
- optional modest interval while Ports tab is visible

The existing five-second refresh model can be reused carefully, but avoid spawning an inspect subprocess per container every cycle if expensive.

---

# 22. Backend API Surface

Do not commit to these exact names, but aim for similarly clean separation.

Potential Wails methods:

```go
func (p *PodmanService) ListContainerNetworking(all bool) ([]ContainerNetworking, error)

func (p *PodmanService) ListHostListeners() ([]HostListener, error)

func (p *PodmanService) GetPortOverview() (*PortOverview, error)

func (p *PodmanService) ValidatePortMapping(req PortMappingRequest) (*PortValidation, error)

func (p *PodmanService) LoadPortRegistry(path string) (*PortRegistryResult, error)

func (p *PodmanService) DetectManagementOrigin(containerID string) (*ManagementOrigin, error)
```

Later:

```go
func (p *PodmanService) UpdateManagedPortMapping(req UpdatePortMappingRequest) (*ChangeResult, error)
```

Do not expose a dangerous generic method such as:

```go
RunShell(string)
```

Keep explicit typed operations.

---

# 23. Internal Package / File Refactor Guidance

The current codebase is small. Do not over-refactor before delivering value.

However, `podman.go` will become unwieldy if every networking feature is appended to it.

A sensible incremental target might become:

```text
main.go
podman.go
podman_test.go

networking.go
networking_test.go

ports.go
ports_test.go

registry.go
registry_test.go

management.go
management_test.go
```

Later, if needed:

```text
internal/
  podman/
  ports/
  registry/
```

Do not force a package migration in the same commit that introduces first networking observation.

Suggested rule:

> Start with separate files in package `main`; extract packages only when boundaries are proven.

---

# 24. Command Execution Testability

The current code already uses injectable helpers in some compose/socket tests, while `PodmanService.runCommand` directly calls `exec.Command`.

Networking work will benefit from testable command/parsing boundaries.

Prefer patterns such as:

```go
func parsePodmanInspect(data []byte) (...)
func parseSSOutput(data string) (...)
func detectConflicts(claims []PortClaim, proposed PortClaim) (...)
```

These can be fully unit-tested without Podman.

For process execution, consider a small runner abstraction only if needed:

```go
type Runner interface {
    Run(name string, args ...string) (stdout string, stderr string, err error)
}
```

Do not introduce dependency injection complexity throughout the app purely for style.

---

# 25. Run Container UX Upgrade

The existing run modal should be upgraded even before editing existing containers.

Replace opaque single-string port input with structured rows.

Example:

```text
Port mappings

Bind address        Host       Container      Protocol
127.0.0.1           8080       80             TCP

[ + Add mapping ]
```

Support multiple mappings.

Before Run:

```text
✓ all mappings valid
✓ no local runtime conflict
✓ no optional registry reservation
```

Generate multiple:

```text
-p ...
-p ...
```

arguments correctly.

Maintain compatibility with existing simple use cases.

---

# 26. Error Handling

Errors should tell the operator what failed and what state remains.

Bad:

```text
Error.
```

Good:

```text
Cannot publish 127.0.0.1:5678/tcp.

The endpoint is already listening on this host.
Podder could not determine the owning process.

No container changes were made.
```

For recreation failure:

```text
Port update failed while creating the replacement container.

Original container:
restored and running

Original mapping:
127.0.0.1:3100 -> 3000/tcp

Requested mapping:
127.0.0.1:3110 -> 3000/tcp

Rollback:
successful
```

---

# 27. Security Considerations

## 27.1 Rootless first

The current UI identifies the environment as rootless/user-session oriented.

Keep rootless Podman as the primary compatibility target.

Do not add routine `sudo` invocation to make networking easier.

## 27.2 Privileged ports

On systems where unprivileged users cannot bind certain ports, detect/report rather than silently escalating.

## 27.3 Local file paths

Registry/Compose/Quadlet file selection must not allow shell injection.

Use direct file I/O and `exec.Command` argument arrays.

## 27.4 Secrets

Do not dump full inspected environment/secrets into logs while implementing adoption/spec export.

If specs eventually include environment configuration, consider secret handling explicitly.

## 27.5 Exposure warnings

Wildcard binds should be visually obvious.

The tool should make it difficult to accidentally turn a loopback-only AI/admin interface into a LAN-accessible service.

---

# 28. Audit Trail

A lightweight local change log is valuable once Podder starts recreating workloads.

Conceptual record:

```json
{
  "time": "2026-08-30T07:45:00+02:00",
  "action": "port-mapping-update",
  "container": "flowise",
  "old": "127.0.0.1:3100->3000/tcp",
  "new": "127.0.0.1:3110->3000/tcp",
  "managementOrigin": "podder",
  "result": "success"
}
```

For failure:

```text
result: failed
rollback: success
```

Do not implement a database solely for this. JSONL under the application's state directory is sufficient initially.

---

# 29. Phased Roadmap

The phases are ordered deliberately.

Do not jump to Phase 6 because it is visually exciting.

---

## Phase 0 — Baseline & Cleanup

### Goal

Establish a known-good repository before networking changes.

### Tasks

- Pull current `main`.
- Read `README.md`, `ABOUT.md`, `CHANGELOG.md`.
- Run existing Go tests.
- Build frontend.
- Build Wails app if environment supports it.
- Record current behavior.
- Do not rename `module changeme` as part of this work.
- Add a small architecture note if necessary.
- Create networking fixtures separately from the owner's malformed registry copy.

### Acceptance

```text
go test ./...        PASS
frontend build       PASS
existing GUI behavior unchanged
```

---

## Phase 1 — Structured Port Discovery

### Goal

Podder accurately knows published container mappings.

### Tasks

- Add `PortMapping` model.
- Extend container/network models without breaking existing JSON parsing.
- Query Podman structured data for published mappings.
- Support:
  - host IP
  - host port
  - container port
  - TCP/UDP
  - multiple mappings
- Add parser fixtures/tests.
- Display mappings on container cards.

### Acceptance

Given:

```text
-p 127.0.0.1:3100:3000
-p 127.0.0.1:5353:5353/udp
```

Podder shows both mappings correctly.

A container with no mappings shows none.

No editing yet.

---

## Phase 2 — Ports Tab + Host Listener Discovery

### Goal

Create a single UI for local port reality.

### Tasks

- Add Ports top-level tab.
- Add `ss`-based listener discovery.
- Normalize host listeners.
- Correlate Podman mappings to observed host endpoints where practical.
- Add filters.
- Add manual refresh.
- Add copy endpoint helpers.
- Do not require process names.

### Acceptance

Podder can visibly distinguish:

```text
Podman-published listener
other host listener
```

and does not label SSH/NFS/etc. as container-managed.

---

## Phase 3 — Conflict & Exposure Engine

### Goal

Answer "Can I safely publish this mapping?"

### Tasks

- Implement `PortClaim`.
- Implement bind overlap logic.
- Handle TCP vs UDP.
- Add conservative IPv4/IPv6 behavior.
- Detect wildcard exposure.
- Add `ValidatePortMapping`.
- Integrate validation into Run Container.
- Add multiple structured port rows to Run Container.
- Add "Find Free Port."

### Acceptance

Podder refuses an exact active conflict.

Podder warns about a wildcard overlap.

Podder does not claim TCP/3000 conflicts merely because UDP/3000 exists.

A loopback -> wildcard proposal is classified as an exposure change.

---

## Phase 4 — Optional External Registry

### Goal

Reconcile intended homelab port state with observed local state.

### Tasks

- Add optional registry path config.
- Parse valid V1 YAML.
- Gracefully report malformed registry.
- Add registry status to Ports tab.
- Add reconciliation states.
- Use active/reserved registry claims in conflict validation.
- Keep registry read-only.

### Acceptance

If registry says:

```text
127.0.0.1:5678/tcp -> n8n
```

and runtime agrees:

```text
MATCH
```

If runtime has an unregistered mapping:

```text
UNDECLARED
```

If registry reserves a free port:

```text
RESERVED
```

Malformed YAML never breaks normal Podder operation.

---

## Phase 5 — Management-Origin Detection

### Goal

Know who owns deployment state before enabling change.

### Tasks

- Detect Pods.
- Detect Podder-managed labels/specs.
- Detect likely Compose ownership.
- Detect likely Quadlet/systemd ownership.
- Classify ad-hoc/unknown.
- Show management badge.
- Add details/evidence view.

### Acceptance

Every container gets a provenance classification or `unknown`.

No unsupported provenance is treated as safely editable.

---

## Phase 6 — Podder-Managed Declarative Specs

### Goal

Give Podder an authoritative recreation source.

### Tasks

- Define V1 managed-container spec.
- Persist specs under XDG config/state.
- Add Podder labels.
- Migrate Run Container so new opted-in durable containers can be Podder-managed.
- Cover every setting exposed by the creation UI.
- Add atomic writes.
- Add spec validation.
- Add recreate-from-spec operation.
- Preserve prior run/stopped state.

### Acceptance

A Podder-managed test container can be:

```text
created
stopped
recreated from spec
started
```

with ports, mounts, command, and supported configuration preserved.

---

## Phase 7 — Safe Edit Port Mapping

### Goal

Make port moves operational for Podder-managed workloads.

### Tasks

- Add Edit Port Mapping dialog.
- Validate before apply.
- Warn on exposure changes.
- Create candidate spec.
- Perform recreate transaction.
- Verify.
- Roll back on failure.
- Add local audit event.
- Make action unavailable for unknown/ad-hoc containers.

### Acceptance

Example test:

```text
127.0.0.1:3100 -> 3000/tcp
```

can safely become:

```text
127.0.0.1:3110 -> 3000/tcp
```

with:

- no data loss
- old mapping gone
- new mapping verified
- all supported container configuration preserved
- rollback if replacement fails

---

## Phase 8 — Compose & Quadlet Integration

### Goal

Apply changes through existing external declarative owners.

### Subphase 8A: Read-only provenance

- discover config
- show file location
- show relevant port declarations

### Subphase 8B: Quadlet editing

- safely edit `PublishPort=`
- preserve file
- reload/restart through correct user/system scope
- verify
- rollback file on failure

### Subphase 8C: Compose editing

Only after a robust YAML-preserving strategy exists.

- identify project/service
- show proposed diff
- safely edit service ports
- `compose up -d`
- verify
- rollback file/deployment if needed

### Acceptance

Podder never bypasses the real deployment owner.

---

## Phase 9 — Adoption

### Goal

Convert suitable ad-hoc containers into Podder-managed declarative workloads.

### Tasks

- complete inspect-to-spec capture
- completeness analysis
- unsupported-setting blocker
- review UI
- explicit adoption
- no silent configuration loss

### Acceptance

Adoption is refused if Podder cannot represent a safety-critical configuration field.

---

## Phase 10 — Broader Networking

Only after the port system is mature.

Candidates:

- Podman network browser
- network membership
- DNS aliases
- Pod networking
- volume/network dependency visualization
- service endpoint graph
- reverse-proxy awareness
- firewall policy hints
- remote Podman hosts

These are not part of the initial port-management implementation.

---

# 30. Suggested Commit / Work-Package Sequence

A good implementation sequence could look like:

```text
1. test: add Podman port mapping fixtures
2. feat: model and discover published port mappings
3. feat: display mappings on container cards
4. feat: add host listener discovery
5. feat: add Ports tab
6. feat: add port conflict engine
7. feat: validate Run Container port mappings
8. feat: support multiple structured mappings
9. feat: add free-port suggestions
10. feat: add optional external port registry
11. feat: add declared-vs-observed reconciliation
12. feat: detect container management origin
13. feat: add Podder-managed container specs
14. feat: recreate Podder-managed container from spec
15. feat: safely edit Podder-managed port mappings
16. feat: add rollback/audit trail
```

Each step should keep tests passing.

---

# 31. Test Strategy

Networking changes need much more than happy-path manual testing.

## 31.1 Unit tests

Test parsing:

- Podman inspect data
- Podman port data if used
- `ss` output
- registry YAML
- malformed registry YAML

Test normalization:

- unspecified bind
- `127.0.0.1`
- `0.0.0.0`
- IPv6
- TCP
- UDP
- multiple mappings

Test conflicts:

```text
specific vs same specific
specific vs wildcard
wildcard vs specific
wildcard vs wildcard
tcp vs udp
different port
different specific IP
IPv4 vs IPv6 conservative cases
```

Test registry state:

```text
active + observed = match
active + missing = declared-missing
no declaration + observed = undeclared
reserved + free = reserved-free
reserved + occupied = reserved-in-use
```

## 31.2 Argument-building tests

Upgrade `buildRunContainerArgs` tests to validate multiple `-p` arguments.

Example expected fragment:

```text
-p 127.0.0.1:8080:80/tcp
-p 127.0.0.1:5353:5353/udp
```

## 31.3 Integration tests

Where a Podman runtime is available:

1. start disposable web container on loopback
2. verify discovery
3. verify host listener correlation
4. verify conflict rejection
5. for managed-spec phase, change port
6. verify old endpoint gone
7. verify new endpoint present
8. verify volume-mounted marker survives recreation

## 31.4 Rollback test

Intentionally make replacement fail.

Verify:

```text
old spec restored
old container/service restored
old port restored
failure reported
```

## 31.5 UI manual checks

- tab navigation
- long IPv6 address rendering
- multiple mappings
- no mappings
- malformed registry warning
- exposure modal
- small window layout
- copy endpoint
- refresh behavior

---

# 32. Definition of Done for the First Major Release

A useful first networking release does **not** need editable existing-container ports.

It is done when:

- container mappings are accurately displayed
- multiple mappings work
- TCP/UDP are correct
- bind IP is visible
- Ports tab exists
- host listeners are visible
- Podman vs non-Podman listener source is distinguishable
- conflict detection exists
- Run Container validates mappings before create
- free-port helper exists
- tests cover bind/protocol conflict semantics
- no existing Podder feature regresses

This alone is a meaningful release.

A subsequent release can add registry reconciliation.

Only a later release should add safe port moves.

---

# 33. Definition of Done for "Port Editing"

Do not call the feature complete unless all of these are true:

- authoritative workload owner known
- full supported deployment configuration available
- proposed mapping validated
- conflict scan passed
- exposure change detected/warned
- previous configuration retained
- container/service redeployed through correct owner
- resulting mapping verified
- prior running/stopped intention preserved
- rollback attempted on failure
- result explained to user
- no persistent data deletion
- automated tests exist

If any of those are missing, the UI should remain read-only for that workload.

---

# 34. Important Non-Goals

Do not expand the first networking project into:

- Kubernetes orchestration
- Docker Swarm
- firewall management
- Proxmox management
- BMC/IPMI configuration
- VLAN configuration
- router configuration
- reverse proxy auto-configuration
- DNS server management
- public ingress automation
- generalized SSH daemon editing
- an all-purpose host service manager
- remote multi-node agent architecture
- secrets manager
- full Compose IDE

Podder may eventually integrate with some of these concepts, but the current goal is local Podman/network endpoint administration.

---

# 35. UX Examples

## 35.1 Container card

```text
┌─────────────────────────────────────────┐
│ flowise                         RUNNING │
│                                         │
│ Image     flowiseai/flowise             │
│ Managed   Podder                        │
│                                         │
│ Ports                                   │
│ 127.0.0.1:3100 → 3000/tcp              │
│                                         │
│ [Logs] [Stop] [Restart] [Networking]    │
└─────────────────────────────────────────┘
```

## 35.2 Ports tab

```text
PORTS

5 published mappings    18 host listeners    1 undeclared

[ All ] [ Podman ] [ Host ] [ Registry ] [ Problems ]

Owner          Endpoint                Target      State
open-webui     127.0.0.1:3000/tcp     8080/tcp    MATCH
flowise        127.0.0.1:3100/tcp     3000/tcp    MATCH
n8n            127.0.0.1:5678/tcp     —           HOST
unknown        0.0.0.0:8099/tcp        —           UNDECLARED
```

## 35.3 Port validation

```text
Proposed mapping

127.0.0.1:3110 -> 3000/tcp

Checks
✓ Valid host port
✓ TCP endpoint free
✓ No active Podman collision
✓ No host listener collision
✓ No registry reservation
✓ Exposure remains loopback

Ready to apply.
```

## 35.4 Unsafe exposure

```text
Exposure change

BEFORE
127.0.0.1:3100

AFTER
0.0.0.0:3100

This may make the service reachable from other machines,
subject to host firewall and network routing.

This is not a simple port-number change.
```

---

# 36. Registry Fixture for Development

Use a clean fixture rather than the owner's currently malformed live file.

Example:

```yaml
version: 1

ports:
  - id: rig9-open-webui
    service: open-webui
    node: rig9
    protocol: tcp
    application_protocol: http
    listener:
      address: 127.0.0.1
      port: 3000
    scope: loopback
    class: application
    state: active
    verification: confirmed
    purpose: Open WebUI local frontend

  - id: rig9-flowise-web
    service: flowise
    node: rig9
    protocol: tcp
    application_protocol: http
    listener:
      address: 127.0.0.1
      port: 3100
    container:
      port: 3000
    scope: loopback
    class: application
    state: active
    verification: confirmed
    purpose: Flowise local frontend

  - id: witness1-relp
    service: witness
    provider: witness1
    protocol: tcp
    application_protocol: relp
    listener:
      port: 2514
    scope: lan
    class: observability
    state: reserved
    verification: confirmed
    purpose: RELP ingestion reservation
```

Tests should also include unknown fields to ensure the optional integration is forward-tolerant.

---

# 37. Potential Port Data Edge Cases

Make explicit decisions for these.

## Random host port

Podman may allow:

```text
127.0.0.1::80
```

where Podman chooses a host port.

The runtime model must support the resulting concrete port.

The creation UI may choose not to support random host ports initially, but the observer must not break when it sees them.

## Ranges

Podman supports port ranges.

V1 can normalize a range into individual mappings if reasonable.

Avoid adding complicated range editing before ordinary mappings are solid.

## IPv6

Examples:

```text
[::1]:8080
[::]:8080
```

Store canonical addresses without destroying the distinction.

## Pods

Published mapping belongs to the Pod.

## Host networking

A container using host networking does not behave like a normal published bridge mapping.

Display network mode clearly and avoid claiming a missing `-p` mapping means the service has no host listeners.

## Pasta/rootless networking

Do not assume every rootless network behaves identically to a rootful bridge.

Use Podman's reported runtime state.

## Stopped containers

A stopped container can retain configured published mappings even when no socket is actively listening.

Distinguish:

```text
CONFIGURED mapping
OBSERVED active listener
```

This is important.

## Wildcard binds

Do not collapse:

```text
0.0.0.0
[::]
unspecified Podman host IP
```

into one display value without understanding semantics.

---

# 38. Configured vs Observed

This distinction should exist in the data model.

Example:

```text
Container configuration:
127.0.0.1:3100 -> 3000/tcp

Runtime listener:
127.0.0.1:3100/tcp
```

For a stopped container:

```text
Configured:
yes

Observed listener:
no

Reason:
container stopped
```

This is **not drift**.

For a running container where configured publication exists but listener is unexpectedly absent, it may be a problem.

The UI should avoid false alarms.

---

# 39. Suggested Architecture of the Port Overview

Conceptually:

```text
Podman inspect/port ----\
                         \
host ss -----------------> Normalize claims ---> Conflict engine
                           |        |
registry.yaml ------------/        +--> Reconciliation
                                    |
                                    +--> UI PortOverview
```

The frontend should receive a useful aggregate model rather than reimplementing networking logic in JavaScript.

Keep policy/normalization/conflict rules in Go.

---

# 40. What Should Stay in Go vs JavaScript

## Go

- Podman command execution
- Podman JSON parsing
- host listener scanning/parsing
- registry file loading/parsing
- endpoint normalization
- conflict logic
- reconciliation
- management provenance
- validation
- managed-spec persistence
- recreation transactions
- rollback
- audit writing

## JavaScript

- rendering
- filtering
- dialogs
- user input collection
- calling typed Wails methods
- displaying validation results
- confirmation UX

Do not duplicate conflict semantics in JavaScript.

---

# 41. Observability and Debugging

When networking discovery fails, make troubleshooting possible.

Potential debug information:

```text
Podman discovery: OK / ERROR
Host listener discovery: OK / ERROR
Registry: disabled / OK / invalid
Last refresh: timestamp
```

Do not fill the normal UI with command stderr, but make useful details available.

Backend errors should wrap context.

Example:

```text
failed to inspect container <id>: ...
```

not:

```text
exit status 125
```

alone.

---

# 42. Performance Guidance

The homelab use case is small, but avoid obviously inefficient polling.

Potential anti-pattern:

```text
every 5 seconds:
  podman ps
  for each of 50 containers:
    podman inspect
    podman port
```

Prefer batch JSON inspection if supported cleanly.

Cache static-ish management provenance separately from frequently changing runtime state.

Host listeners can be rescanned when the Ports tab is visible.

Correctness beats micro-optimization, but subprocess count should remain reasonable.

---

# 43. Backward Compatibility

Preserve:

- current start/stop/restart/remove behavior
- logs
- images
- build
- pull
- compose up/down
- native Podman CLI passthrough
- rootless operation
- current basic run-container use cases

If changing `RunContainer` Wails method signatures, update generated bindings and frontend atomically.

Consider preserving an internal compatibility wrapper while migrating from opaque `ports string` to structured mappings.

---

# 44. Documentation Deliverables

As features land, update:

## README

Add:

- Networking/Ports feature
- screenshot if appropriate
- rootless listener limitations
- optional registry configuration
- what "Edit Port Mapping" actually does

## ABOUT

Update capabilities once stable.

## CHANGELOG

Use staged entries under Unreleased.

Example:

```text
Added
- Container published-port discovery.
- Ports overview with host-listener reconciliation.
- Port collision validation.

Changed
- Run Container now supports multiple structured port mappings.
```

## Internal docs

Add a short architecture document if the feature surface becomes substantial.

Potential:

```text
docs/networking.md
```

---

# 45. Release Strategy

Do not bundle every phase into one release.

A sensible release progression could be:

```text
1.2.x
Networking visibility + Ports tab + conflict checks

1.3.x
Optional external registry reconciliation

1.4.x
Management provenance + Podder-managed specs

1.5.x
Safe Podder-managed port editing/rollback

later
Compose/Quadlet editing + adoption
```

Version numbers are suggestions, not requirements.

---

# 46. Immediate Work Order for ChatGPT Work

If asked to begin implementing now, use this order:

## Step A

Inspect current repository and run baseline tests/build.

Report any discrepancies with this primer.

## Step B

Create a clean design note/issue checklist from Phases 1-3.

Do not change deployment behavior yet.

## Step C

Implement structured `PortMapping` parsing/discovery with tests.

## Step D

Display mappings on container cards.

## Step E

Add Ports tab.

## Step F

Add host listener discovery.

## Step G

Implement pure conflict engine with exhaustive tests.

## Step H

Refactor Run Container to structured, multi-port inputs and validate them.

Stop after this coherent milestone unless explicitly instructed to continue.

At that point the owner should have a useful safe networking release before port editing is attempted.

---

# 47. Questions Work Should Resolve From Code, Not Ask the User Prematurely

Prefer repository inspection and sensible defaults for:

- exact Podman inspect JSON shape
- Wails generated binding workflow
- current CSS component patterns
- whether `ss` exists on supported Linux baseline
- current build commands in Taskfile/workflows
- best file boundary for networking code
- compose labels available from Podman inspection
- existing release/test CI expectations

Only require owner input for genuine product decisions such as:

- whether Podder should write the external registry
- preferred config file format for Podder-managed specs
- whether a specific existing container should be adopted
- whether an exposure change is desired
- whether automatic Compose mutation should be enabled

---

# 48. Explicit Anti-Patterns

Do not implement any of these:

## Anti-pattern 1

```text
"Port 3000 is in use"
```

without specifying protocol/address/context.

## Anti-pattern 2

Treat image `EXPOSE 3000` as a host publication.

## Anti-pattern 3

Assume every `localhost` in an external registry refers to the Podder desktop host.

## Anti-pattern 4

Delete and reconstruct an arbitrary container from a partial inspect conversion.

## Anti-pattern 5

Modify a Compose-managed container directly while leaving Compose unchanged.

## Anti-pattern 6

Modify a Quadlet-managed container directly while leaving the unit unchanged.

## Anti-pattern 7

Silently change loopback to wildcard.

## Anti-pattern 8

Treat a stopped configured mapping as erroneous simply because `ss` does not see a listener.

## Anti-pattern 9

Make optional `ports.yaml` parsing failure crash core Podder.

## Anti-pattern 10

Let YAML rewrite logic destroy comments in the Git registry.

## Anti-pattern 11

Hard-code the owner's homelab into the public app.

## Anti-pattern 12

Move networking policy/conflict logic into frontend JavaScript.

---

# 49. Long-Term Vision

If this roadmap succeeds, Podder can eventually provide a compact local control-plane view:

```text
                         PODDER
                          |
        +-----------------+------------------+
        |                 |                  |
    Runtime            Declared           Safety
    Podman             Podder specs       conflict checks
    listeners          Compose            exposure checks
    networks           Quadlet            rollback
                       port registry
        |                 |                  |
        +-----------------+------------------+
                          |
                     Reconciliation
                          |
             +------------+------------+
             |                         |
          Human UI                  CLI/API
```

That creates a compelling niche:

- simpler than large cluster platforms
- safer than raw ad-hoc `podman run`
- more declarative than a basic container GUI
- still native to local Podman/systemd workflows
- capable of integrating with a Git-managed homelab source of truth

---

# 50. Final Implementation Principle

The central idea to preserve through every phase is:

> **Observation comes before mutation. Ownership comes before recreation. Validation comes before applying. Verification and rollback are part of applying.**

For ports specifically:

> A "port" is never just an integer. It is an endpoint claim with address, protocol, ownership, exposure, lifecycle, and deployment consequences.

If Podder preserves those two principles, it can safely grow from a useful lightweight GUI into a much more capable local Podman administration tool without losing its simplicity.
