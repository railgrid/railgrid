import { readdir } from 'node:fs/promises'

/**
 * Return every file below a Vite output directory. Keep this recursive so the
 * total production budget cannot silently omit a newly introduced chunk,
 * stylesheet, font, image, or deeper asset directory.
 */
export async function listBuildArtifacts(rootURL) {
  const artifacts = []

  async function walk(directoryURL, prefix) {
    const entries = await readdir(directoryURL, { withFileTypes: true })
    for (const entry of entries) {
      const name = `${prefix}${entry.name}`
      if (entry.isDirectory()) {
        await walk(new URL(`${encodeURIComponent(entry.name)}/`, directoryURL), `${name}/`)
      } else if (entry.isFile()) {
        artifacts.push(name)
      }
    }
  }

  await walk(rootURL, '')
  return artifacts.sort((left, right) => left.localeCompare(right, 'en'))
}

export function totalArtifactSize(items) {
  return items.reduce(
    (sum, item) => ({ rawBytes: sum.rawBytes + item.rawBytes, gzipBytes: sum.gzipBytes + item.gzipBytes }),
    { rawBytes: 0, gzipBytes: 0 },
  )
}

export function manifestEntryKey(manifest, predicate, label) {
  const matches = Object.entries(manifest)
    .filter(([, entry]) => predicate(entry))
    .map(([key]) => key)
  if (matches.length !== 1) {
    throw new Error(`expected one ${label} manifest entry, found: ${matches.join(', ') || 'none'}`)
  }
  return matches[0]
}

/** Collect one Rollup entry's emitted file, CSS/assets, and static imports. */
export function collectManifestEntryArtifacts(manifest, entryKey) {
  const artifacts = new Set()
  const visited = new Set()

  function visit(key) {
    if (visited.has(key)) return
    visited.add(key)
    const entry = manifest[key]
    if (!entry || typeof entry.file !== 'string') {
      throw new Error(`manifest entry ${key} is missing an emitted file`)
    }
    artifacts.add(entry.file)
    for (const name of entry.css ?? []) artifacts.add(name)
    for (const name of entry.assets ?? []) artifacts.add(name)
    for (const importedKey of entry.imports ?? []) visit(importedKey)
  }

  visit(entryKey)
  return [...artifacts].sort((left, right) => left.localeCompare(right, 'en'))
}
