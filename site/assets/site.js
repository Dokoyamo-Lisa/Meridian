// Meridian's guide: the look menu, the menu on phones, copy buttons, search over the guide, the
// page's contents following the reading, and screenshots full size on a click.
;(function () {
  'use strict'
  var root = document.documentElement
  var $ = function (s, r) { return (r || document).querySelector(s) }
  var $$ = function (s, r) { return Array.prototype.slice.call((r || document).querySelectorAll(s)) }
  var store = {
    get: function (k) { try { return localStorage.getItem(k) } catch (e) { return null } },
    set: function (k, v) { try { v == null ? localStorage.removeItem(k) : localStorage.setItem(k, v) } catch (e) {} },
  }

  // ---------------------------------------------------------------- the look
  var toneBtn = $('[data-tone-btn]')
  var pop = $('.tone-pop')
  var looks = ['umbrella', 'romance']
  function chosen() {
    var t = store.get('meridian.site.tone')
    return looks.indexOf(t) >= 0 ? t : 'umbrella'
  }
  function apply(t) {
    root.setAttribute('data-theme', t)
    $$('button', pop).forEach(function (b) { b.setAttribute('aria-checked', String(b.dataset.tone === t)) })
  }
  if (toneBtn && pop) {
    var close = function () { pop.hidden = true; toneBtn.setAttribute('aria-expanded', 'false') }
    toneBtn.addEventListener('click', function (e) {
      e.stopPropagation()
      pop.hidden = !pop.hidden
      toneBtn.setAttribute('aria-expanded', String(!pop.hidden))
      if (!pop.hidden) ($('[aria-checked="true"]', pop) || $('button', pop)).focus()
    })
    $$('button', pop).forEach(function (b) {
      b.addEventListener('click', function () {
        store.set('meridian.site.tone', b.dataset.tone === 'umbrella' ? null : b.dataset.tone)
        apply(b.dataset.tone)
        close()
      })
    })
    document.addEventListener('click', function (e) { if (!pop.hidden && !e.target.closest('.tone')) close() })
    document.addEventListener('keydown', function (e) { if (e.key === 'Escape' && !pop.hidden) { close(); toneBtn.focus() } })
  }

  // ---------------------------------------------------------------- the menu on phones
  var navBtn = $('.nav-toggle')
  var nav = $('#nav')
  if (navBtn && nav) {
    navBtn.addEventListener('click', function () {
      var open = nav.classList.toggle('open')
      navBtn.setAttribute('aria-expanded', String(open))
    })
    $$('a', nav).forEach(function (a) { a.addEventListener('click', function () { nav.classList.remove('open') }) })
  }

  // ---------------------------------------------------------------- copy buttons
  var copyIcon = '<svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M9 9h11v11H9zM5 15H4V4h11v1"/></svg>'
  var doneIcon = '<svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M5 12.5l4.5 4.5L19 7.5"/></svg>'
  function copied(btn) {
    btn.innerHTML = doneIcon
    setTimeout(function () { btn.innerHTML = copyIcon }, 1400)
  }
  function copy(text, btn) {
    if (navigator.clipboard) navigator.clipboard.writeText(text).then(function () { copied(btn) })
  }
  $$('pre').forEach(function (pre) {
    var b = document.createElement('button')
    b.type = 'button'
    b.className = 'icon-btn copy'
    b.setAttribute('aria-label', 'Copy')
    b.innerHTML = copyIcon
    b.addEventListener('click', function () { copy(pre.innerText.replace(/\n$/, ''), b) })
    pre.appendChild(b)
  })

  // ---------------------------------------------------------------- search
  var dlg = $('.search')
  var input = dlg && $('input', dlg)
  var hits = dlg && $('.search-hits', dlg)
  var base = dlg ? dlg.getAttribute('data-root') : ''
  var index = null
  var sel = 0
  function load() {
    if (index) return Promise.resolve(index)
    return fetch(base + 'search.json').then(function (r) { return r.json() }).then(function (j) { index = j; return j })
  }
  function openSearch() {
    if (!dlg) return
    dlg.hidden = false
    input.value = ''
    hits.innerHTML = ''
    input.focus()
    load()
  }
  function closeSearch() { if (dlg) dlg.hidden = true }
  function esc(s) { return s.replace(/[&<>"]/g, function (c) { return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c] }) }
  function mark(s, words) {
    var out = esc(s)
    words.forEach(function (w) {
      if (w.length < 2) return
      out = out.replace(new RegExp('(' + w.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + ')', 'ig'), '<mark>$1</mark>')
    })
    return out
  }
  function find() {
    var q = input.value.trim().toLowerCase()
    if (!q || !index) { hits.innerHTML = ''; return }
    var words = q.split(/\s+/)
    var found = []
    index.forEach(function (e) {
      var hay = (e.h + ' ' + e.x + ' ' + e.t).toLowerCase()
      if (!words.every(function (w) { return hay.indexOf(w) >= 0 })) return
      var score = words.reduce(function (s, w) { return s + (e.h.toLowerCase().indexOf(w) >= 0 ? 3 : 0) + (e.t.toLowerCase().indexOf(w) >= 0 ? 1 : 0) }, 0)
      found.push({ e: e, s: score })
    })
    found.sort(function (a, b) { return b.s - a.s })
    sel = 0
    hits.innerHTML = found.slice(0, 12).map(function (f, i) {
      var e = f.e
      var at = e.x.toLowerCase().indexOf(words[0])
      var snip = at > 40 ? '…' + e.x.slice(at - 30, at + 110) : e.x.slice(0, 140)
      var page = e.p === 'index' ? 'index.html' : 'guide/' + e.p + '.html'
      return '<li><a href="' + base + page + '#' + e.id + '"' + (i === 0 ? ' class="on"' : '') + '><b>' + mark(e.h, words) + '</b><small>' + esc(e.t) + ' · ' + mark(snip, words) + '</small></a></li>'
    }).join('') || '<li><small style="display:block;padding:12px">Nothing found - try other words.</small></li>'
  }
  if (dlg) {
    $$('[data-search]').forEach(function (b) { b.addEventListener('click', openSearch) })
    input.addEventListener('input', function () { load().then(find) })
    dlg.addEventListener('click', function (e) { if (e.target === dlg) closeSearch() })
    dlg.addEventListener('keydown', function (e) {
      var links = $$('a', hits)
      if (e.key === 'Escape') closeSearch()
      else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault()
        if (!links.length) return
        links[sel] && links[sel].classList.remove('on')
        sel = (sel + (e.key === 'ArrowDown' ? 1 : links.length - 1)) % links.length
        links[sel].classList.add('on')
        links[sel].scrollIntoView({ block: 'nearest' })
      } else if (e.key === 'Enter' && links[sel]) {
        e.preventDefault()
        location.href = links[sel].href
        closeSearch()
      }
    })
    document.addEventListener('keydown', function (e) {
      if ((e.key === '/' || (e.key === 'k' && (e.metaKey || e.ctrlKey))) && dlg.hidden && !/input|textarea/i.test((e.target.tagName || ''))) {
        e.preventDefault()
        openSearch()
      }
    })
  }

  // ---------------------------------------------------------------- the page's contents follow the reading
  var toc = $$('.toc a')
  if (toc.length && 'IntersectionObserver' in window) {
    var heads = toc.map(function (a) { return document.getElementById(decodeURIComponent(a.hash.slice(1))) }).filter(Boolean)
    var io = new IntersectionObserver(function (entries) {
      entries.forEach(function (en) {
        if (!en.isIntersecting) return
        toc.forEach(function (a) { a.classList.toggle('on', a.hash === '#' + en.target.id) })
      })
    }, { rootMargin: '-70px 0px -70% 0px' })
    heads.forEach(function (h) { io.observe(h) })
  }

  // ---------------------------------------------------------------- screenshots, full size on a click
  var zoom = null
  function closeZoom() { if (zoom) { zoom.remove(); zoom = null } }
  $$('.prose img').forEach(function (img) {
    img.addEventListener('click', function () {
      zoom = document.createElement('div')
      zoom.className = 'zoom'
      zoom.setAttribute('role', 'dialog')
      zoom.setAttribute('aria-label', img.alt || 'Screenshot')
      var big = document.createElement('img')
      big.src = img.currentSrc || img.src
      big.alt = img.alt
      zoom.appendChild(big)
      zoom.addEventListener('click', closeZoom)
      document.body.appendChild(zoom)
    })
  })
  document.addEventListener('keydown', function (e) { if (e.key === 'Escape') closeZoom() })

  apply(chosen())
})()
