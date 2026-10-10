// The look, before the first paint: the one chosen here before, else Romance - Rosélune's own. The tab's
// icon follows: the rose, or the umbrella in the Umbrella look.
;(function () {
  var t = null
  try {
    t = localStorage.getItem('meridian.site.tone')
  } catch (e) {}
  if (t !== 'umbrella') t = 'romance'
  document.documentElement.setAttribute('data-theme', t)
  var icon = document.querySelector('link[rel="icon"][data-umbrella]')
  if (icon && t === 'umbrella') icon.href = icon.getAttribute('data-umbrella')
})()
