# Client Identity Binding Remediation

## Problem

Vulnerability report (CWE-639 / CWE-862, CVSS 9.9) against `9019a0f`:
`POST /sessions/{id}/stdin`, `GET /sessions/{id}/{m_type}/poll`, and
`GET /sessions/{id}/{m_type}/ack` take the caller's client identity from the
`client_id` query parameter and never bind it to the authenticated identity
(`GetAuthContext(r).ClientID`). Because the host record is registered under the
literal id `host` with `ApprovalApproved`, any valid client certificate can send
`?client_id=host` and inject stdin into the host's wrapped process (RCE as the
host user).

Related gaps with the same root cause:

* `Session.RegisterClient` reuses an existing client record whenever the caller
  supplies its id (`POST /sessions/{id}/clients?client_id=<uuid>` and the
  WebSocket upgrade). A caller who learns another client's UUID can take over
  that record, including its approval and permission.
* `handlePoll` performs no approval check, so `client.waitForApproval` (which
  assumes a poll error means "not yet approved") returns immediately.
* `client.pollOutput` polls the output queue with `types.HostClientID` instead
  of its own client id.
* `apiclient.EnqueueStdin` ignores its `source` argument and never sends
  `client_id`, so HTTP-fallback stdin is always refused when approval is
  required.

## Why not `clientID = authCtx.ClientID`

The report suggests replacing the query parameter with the authenticated
identity. That doesn't work here: non-host client ids are server-generated UUIDs
returned from registration, and are unrelated to the certificate CN (or proxy
identity header). Several clients may share one certificate. The `client_id`
parameter is therefore still needed to pick *which* client record. What is
missing is a binding between the record and the principal that created it.

## Design

1. **Record owner.** `SessionClient` gains an unexported `owner string` field,
   set at creation to the authenticated principal (`authCtx.ClientID`, or `""`
   when there is no auth context), with an `Owner()` accessor.
   `Session.RegisterClient` takes the owner as a parameter. The reuse path only
   reuses an existing record when `existing.owner == owner`; otherwise it falls
   through and creates a fresh pending client. The host record is not
   owner-tracked; it stays gated by the existing `authCtx.ClientID ==
   types.HostClientID` checks.
2. **Route-level authorization helper** in `routes.go`, used by the poll, ack and
   stdin routes before they call their handlers:
   * no auth context, or `AuthModeNone`: allow (matches existing guards)
   * `client_id == host`: allow only if `authCtx.ClientID == types.HostClientID`
   * otherwise: if the session and client record exist, allow only if
     `client.Owner() == authCtx.ClientID`; else `403`.
   * Unknown sessions and unknown or empty client ids are passed through so the
     handlers' existing 404/403 behaviour is unchanged.
3. **Poll approval.** When `RequireApproval` is set, `handlePoll` refuses (403)
   a non-host client that is missing or not approved for read. Ack only needs
   ownership.
4. **Client fixes.** `pollOutput` uses `c.clientID`. `apiclient.EnqueueStdin`
   sends `?client_id=<source>` (query-escaped) when `source` is non-empty.
   `apiclient.Poll`/`Ack` query-escape the id.

The WebSocket message path (`handleStdinSubmitWS`) needs no change. Its
`clientID` comes from registration at upgrade time, and the owner binding in (1)
already covers that registration.

## Out of scope / follow-ups

* With `require_approval=false`, `handleEnqueueStdin` does not enforce
  `default_permission=read-only`, so read-only clients can still write stdin.
* Host identity is "any cert with CN=host". Operators must not issue client
  certs with that CN.
