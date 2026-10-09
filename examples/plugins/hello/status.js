// The Hello plugin on the status page: a star before each server's name on the Servers page. It runs
// in the browser of everyone who opens the status page and uses window.MeridianStatus
// (docs/plugins.md).
;(function () {
  const S = window.MeridianStatus
  if (!S) return

  // called whenever a server's card is drawn or updated - often, so it changes nothing twice
  S.decorateCard(function (card, server) {
    const name = card.querySelector('.c-t b')
    if (name && !name.textContent.startsWith('★ ')) name.textContent = '★ ' + server.name
  })
})()
