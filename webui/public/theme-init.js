;(function () {
  try {
    var stored = localStorage.getItem('hakase_theme')
    var dark = stored === null || stored === 'dark'
    document.documentElement.classList.toggle('dark', dark)
  } catch (e) {}
})()
