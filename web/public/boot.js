// Applies the saved tone before the first paint, so pages never flash the wrong colours.
(function () {
  var t = null
  try {
    t = localStorage.getItem('meridian.tone')
  } catch (e) {}
  if (!/^(ice|celadon|ink|paper|mist)$/.test(t || '')) {
    t = window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches ? 'paper' : 'ice'
  }
  document.documentElement.setAttribute('data-theme', t)
})()
