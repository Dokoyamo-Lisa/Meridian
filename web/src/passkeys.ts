// Passkeys in the browser: the panel's options (binary fields in base64url) into navigator.credentials,
// and the passkey's answer back to the panel as JSON (internal/panel/passkeys.go).

import { Account, post } from './api'
import { errText } from './ui'

function fromB64url(s: string): ArrayBuffer {
  const b = atob(s.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4 - (s.length % 4)) % 4))
  const out = new Uint8Array(b.length)
  for (let i = 0; i < b.length; i++) out[i] = b.charCodeAt(i)
  return out.buffer
}

function toB64url(buf: ArrayBuffer): string {
  let s = ''
  for (const c of new Uint8Array(buf)) s += String.fromCharCode(c)
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

/** passkeysWork says whether this browser can use passkeys here: they need the panel's own name, never an IP address. */
export function passkeysWork(): boolean {
  const h = location.hostname
  return typeof window.PublicKeyCredential === 'function' && !!navigator.credentials && !/^[\d.]+$/.test(h) && !h.includes(':') && !h.startsWith('[')
}

type Descriptor = { id: string; type: string; transports?: string[] }

function creationOptions(pk: any): PublicKeyCredentialCreationOptions {
  return {
    ...pk,
    challenge: fromB64url(pk.challenge),
    user: { ...pk.user, id: fromB64url(pk.user.id) },
    excludeCredentials: (pk.excludeCredentials || []).map((c: Descriptor) => ({ ...c, id: fromB64url(c.id) })),
  }
}

function requestOptions(pk: any): PublicKeyCredentialRequestOptions {
  return { ...pk, challenge: fromB64url(pk.challenge), allowCredentials: (pk.allowCredentials || []).map((c: Descriptor) => ({ ...c, id: fromB64url(c.id) })) }
}

function answer(c: PublicKeyCredential): unknown {
  const r = c.response as AuthenticatorAttestationResponse & AuthenticatorAssertionResponse
  const out: any = {
    id: c.id,
    rawId: toB64url(c.rawId),
    type: c.type,
    authenticatorAttachment: c.authenticatorAttachment || undefined,
    clientExtensionResults: c.getClientExtensionResults ? c.getClientExtensionResults() : {},
    response: { clientDataJSON: toB64url(r.clientDataJSON) },
  }
  if ('attestationObject' in r && r.attestationObject) {
    out.response.attestationObject = toB64url(r.attestationObject)
    out.response.transports = typeof r.getTransports === 'function' ? r.getTransports() : []
  }
  if ('authenticatorData' in r && r.authenticatorData) {
    out.response.authenticatorData = toB64url(r.authenticatorData)
    out.response.signature = toB64url(r.signature)
    if (r.userHandle) out.response.userHandle = toB64url(r.userHandle)
  }
  return out
}

/** addPasskey makes a passkey on this device (or the person's password manager) for the signed-in account. */
export async function addPasskey(name: string) {
  const begun = await post<{ ceremony: string; options: { publicKey: any } }>('/api/me/passkeys/begin')
  const cred = (await navigator.credentials.create({ publicKey: creationOptions(begun.options.publicKey) })) as PublicKeyCredential | null
  if (!cred) throw new Error('No passkey was made.')
  return post('/api/me/passkeys/finish', { ceremony: begun.ceremony, name, credential: answer(cred) })
}

/** passkeySignIn signs in with any of this panel's passkeys the person picks. */
export async function passkeySignIn() {
  const begun = await post<{ ceremony: string; options: { publicKey: any } }>('/api/login/passkey/begin')
  const cred = (await navigator.credentials.get({ publicKey: requestOptions(begun.options.publicKey) })) as PublicKeyCredential | null
  if (!cred) throw new Error('No passkey was used.')
  return post<{ kind?: 'admin' | 'user'; account?: Account }>('/api/login/passkey/finish', { ceremony: begun.ceremony, credential: answer(cred) })
}

/** passkeyError says what went wrong in plain words: the browser's own errors are technical. */
export function passkeyError(e: unknown): string {
  switch ((e as { name?: string })?.name) {
    case 'NotAllowedError':
      return 'No passkey was used - it was cancelled, or it took too long. Try again.'
    case 'InvalidStateError':
      return 'This device has a passkey for this panel already.'
    case 'SecurityError':
      return 'Passkeys work only at the panel’s own address (its domain, over HTTPS).'
    case 'NotSupportedError':
      return 'This browser or device cannot make passkeys.'
  }
  return errText(e)
}
