---
layout: default
title: OIDC identity linking
description: "Keep railgrid accounts intact across a Dex connector or IdP change"
---

# OIDC identity linking

A railgrid account (a `User`) is found at sign-in by the IdP identity that
signed in: `sha256(issuer + "/" + sub)`. Two things change that pair for the
same person:

- **A new Dex connector.** Dex encodes the connector ID into `sub`, so the same
  person signing in with Google instead of GitHub is a different subject, even
  though the issuer is the same.
- **A new IdP.** Moving off Dex (or onto it) changes the issuer and every `sub`.

Without linking, either change gives every returning person a fresh, empty
`User`; their organization memberships and app-access grants stay on the old
one. Identity linking lets one `User` hold several identities and attaches a
new identity to an existing account on first sign-in.

kcp is not affected either way: it authenticates users as `railgrid:<email>`,
which does not depend on issuer or `sub`.

## How a sign-in resolves to a User

1. A `User` whose `tenants.railgrid.ai/sub` label (its primary identity, the one
   it was created with) or one of its `identities.tenants.railgrid.ai/<hash>`
   labels (identities linked later) matches the identity hash. Found: done.
2. **Linking** (only for issuers in `--idp-link-verified-email-issuers`): if
   the ID token carries `email_verified: true` and exactly one existing,
   IdP-bound `User` has the same email (case-insensitive), presented by one
   of its own identities and not recorded there as unverified, the identity
   is linked to it: it gets a `identities.tenants.railgrid.ai/<hash>: "true"`
   label and a `spec.oidcProviders` entry recording issuer, subject, email
   and whether the issuer verified it.
3. **Invite adoption**: a pending invited `User` with that email and no IdP
   identity yet is bound to the identity.
4. Otherwise a new `User` is created.

Linking refuses, and falls through to steps 3–4, when the email is not
verified, when the issuer is not listed, when several accounts share the
email, or when the account's own identity recorded that email as unverified.
Static-token users are never linked. Two sign-ins racing to bind the same new
identity land on the same account: the one that loses the write looks the
identity up again.

## What an identity records

Every `spec.oidcProviders` entry holds the email its identity presented and
`emailVerified`, the issuer's `email_verified` claim when the identity was
bound or when its email last changed; a later sign-in that verifies the same
email sets it to `true`. An account whose matching entry says
`emailVerified: false` is not a linking target, so an account seeded through a
connector that does not verify emails cannot capture the identity of whoever
owns that email. Entries written before the field existed carry no verdict and
are trusted as they always were, which keeps accounts from before this hub
version linkable during an IdP move even if they never sign in again on the
old IdP.

The stored spelling of an email is kept when a sign-in differs only in case:
kcp RBAC is bound to `railgrid:<email>` as stored, and rewriting it on every
sign-in would flip it between two identities that spell the email
differently. kcp itself sees the spelling in the token, so identities that
should share workspace RBAC must present the email with the same case.

## Configuration

| Hub flag | Chart value | Default |
| -------- | ----------- | ------- |
| `--idp-link-verified-email-issuers` | `idp.linkVerifiedEmailIssuers` | empty: linking off |

Listing an issuer trusts it to have verified email ownership for every
connector it fronts. For Dex that means every configured connector must only
assert verified emails (the GitHub and Google connectors do). Leave the list
empty unless you are adding a connector or migrating IdPs.

## Runbook: adding a Dex connector

1. Add the connector to Dex.
2. Set `idp.linkVerifiedEmailIssuers: [<dex issuer>]` on the hub.
3. People who sign in through the new connector land on their existing
   account, provided their email there matches.

Linking can stay on afterwards; it only acts on identities no account holds
yet.

## Runbook: moving to another IdP

1. Register the hub's public client (`idp.clientID`, PKCE, redirect
   `<hub>/auth/callback` and `http://localhost:8000`) in the new IdP. It must
   put `email` and `email_verified` in the ID token.
2. On the hub: set `idp.issuerURL` to the new issuer and add it to
   `idp.linkVerifiedEmailIssuers`.
3. On kcp (front proxy and every shard): point the OIDC `issuerURL` and
   `clientID` at the new IdP. Keep `usernameClaim: email` and the `railgrid:`
   prefix; that keeps workspace RBAC valid. RBAC bound to IdP groups
   (`groupsClaim`) needs rewriting if the new IdP names groups differently.
4. Everyone signs in again (old refresh tokens are dead). Each first sign-in
   links the new identity to the existing account.

Because the issuer hostname is configuration, an IdP that can serve your own
domain as its issuer (e.g. a custom domain at a hosted IdP) lets you skip the
kcp part of step 3 except for `clientID`.
