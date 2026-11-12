// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Organization creation retries carry a version 7 UUID: the hub reads the
// time it was minted and refuses a key old enough to outlive its organization.
export function requestIdV7(now = Date.now(), random: Uint8Array = crypto.getRandomValues(new Uint8Array(16))): string {
  const bytes = Uint8Array.from(random.subarray(0, 16))
  let ms = Math.floor(now)
  for (let i = 5; i >= 0; i--) {
    bytes[i] = ms % 256
    ms = Math.floor(ms / 256)
  }
  bytes[6] = (bytes[6] & 0x0f) | 0x70
  bytes[8] = (bytes[8] & 0x3f) | 0x80
  const hex = Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}

// requestIdMintedAt returns the millisecond timestamp of a version 7 UUID, or
// undefined for anything else.
export function requestIdMintedAt(id: string): number | undefined {
  if (!/^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(id)) return undefined
  return parseInt(id.slice(0, 8) + id.slice(9, 13), 16)
}

// A draft retries with its own key only while the hub will still accept it.
export const REQUEST_ID_REUSE_MS = 60 * 60 * 1000

export function reusableRequestId(id: string | undefined, now = Date.now()): string | undefined {
  const minted = id ? requestIdMintedAt(id) : undefined
  return minted !== undefined && now - minted < REQUEST_ID_REUSE_MS && minted - now < REQUEST_ID_REUSE_MS ? id : undefined
}
