// Applies the saved tone before the first paint, so pages never flash the wrong colours. Without
// one, the site's own look (data-default-tone, set by the panel) - else Ice, or Paper on a device
// set to light.
(function () {
  var ok = /^(ice|celadon|ink|paper|mist|umbrella|romance)$/
  var root = document.documentElement
  var t = null
  try {
    t = localStorage.getItem('meridian.tone')
  } catch (e) {}
  if (!ok.test(t || '')) t = root.getAttribute('data-default-tone')
  if (!ok.test(t || '')) {
    t = window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches ? 'paper' : 'ice'
  }
  root.setAttribute('data-theme', t)
})()
