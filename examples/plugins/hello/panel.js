// The Hello plugin in the panel: a page with what its program counted, and an entry in the account
// menu that leads there. It runs in the supervisor's browser and uses window.Meridian
// (docs/plugins.md). Everything it shows goes in through textContent, never as HTML.
;(function () {
  const M = window.Meridian
  if (!M) return

  function el(tag, cls, text) {
    const e = document.createElement(tag)
    if (cls) e.className = cls
    if (text !== undefined) e.textContent = text
    return e
  }

  function when(ts) {
    return ts ? new Date(ts * 1000).toLocaleString() : 'not yet'
  }

  M.addPage({
    path: '/hello',
    title: 'Hello',
    render(root) {
      const box = el('section', 'panel')
      root.append(box)
      async function show() {
        let s
        try {
          s = await M.api('GET', '/api/plugins/hello/stats')
        } catch (e) {
          box.replaceChildren(el('p', 'muted', 'The Hello plugin did not answer: ' + e.message))
          return
        }
        const rows = Object.entries(s.events || {}).sort((a, b) => b[1] - a[1])
        const list = el('div', 'kv-list')
        list.append(
          el('div', '', 'Counting since ' + when(s.since)),
          el('div', '', 'Servers at the last count: ' + s.servers + ' (' + when(s.counted_at) + ')'),
          el('div', '', 'Subscriptions with stars: ' + s.starred),
        )
        const table = el('table', 't')
        const body = el('tbody')
        for (const [kind, n] of rows) {
          const tr = el('tr')
          tr.append(el('td', 'mono', kind), el('td', 'right', String(n)))
          body.append(tr)
        }
        table.append(body)
        box.replaceChildren(el('h2', 'h', 'Events by kind'), list, rows.length ? table : el('p', 'muted', 'No events yet.'))
      }
      void show()
      const timer = setInterval(show, 10000)
      return () => clearInterval(timer) // called when the supervisor leaves the page
    },
  })

  M.addMenuItem({ label: 'Hello: what it counted', run: () => M.navigate('/plugin/hello') })
})()
