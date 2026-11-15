const terms = query => query.toLowerCase().trim().split(/\s+/).filter(Boolean)

export function matchesText(text, query) {
  const normalized = text.toLowerCase()
  return terms(query).every(term => normalized.includes(term))
}

export function matchesEntry(doc, query) {
  return matchesText([
    doc.title, doc.id, doc.review.title, doc.review.tryThis, doc.review.product?.label ?? '',
    ...doc.sources.map(item => item.path), ...doc.review.sources.map(item => item.path),
    ...doc.adoption.map(item => `${item.label} ${item.path}`), ...doc.sections.map(item => item.title),
  ].join(' '), query)
}
