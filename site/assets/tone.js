// The look, before the first paint: the one chosen here before, else Umbrella - the guide's own.
;(function () {
  var t = null
  try {
    t = localStorage.getItem('meridian.site.tone')
  } catch (e) {}
  if (t !== 'romance') t = 'umbrella'
  document.documentElement.setAttribute('data-theme', t)
})()
