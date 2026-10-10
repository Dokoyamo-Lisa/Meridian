// Applies the saved look before the first paint, so pages never flash the wrong colours. Without
// one, the site's own look (data-default-tone, set by the panel): Romance unless another was chosen,
// and "auto" is Ice, or Paper on a device set to light. The colour browsers paint around the page
// (theme-color) follows.
(function () {
  var page = { romance: '#fbf0f2', umbrella: '#0a0a0b', ice: '#07090d', celadon: '#111413', ink: '#13110f', paper: '#f3efe6', mist: '#eef2f1' }
  var root = document.documentElement
  var t = null
  try {
    t = localStorage.getItem('meridian.tone')
  } catch (e) {}
  if (!page.hasOwnProperty(t || '')) t = root.getAttribute('data-default-tone')
  if (t === 'auto') t = window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches ? 'paper' : 'ice'
  if (!page.hasOwnProperty(t || '')) t = 'romance'
  root.setAttribute('data-theme', t)
  var m = document.querySelector('meta[name="theme-color"]')
  if (!m && document.head) {
    m = document.createElement('meta')
    m.name = 'theme-color'
    document.head.appendChild(m)
  }
  if (m) m.content = page[t]
})()
