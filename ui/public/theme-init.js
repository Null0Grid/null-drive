// Runs before the app loads so a light-mode user never sees a dark flash.
(function () {
  try {
    var d = document.documentElement;
    if (localStorage.getItem('nd.theme') === 'light') d.dataset.theme = 'light';
    if (localStorage.getItem('nd.scanlines') === 'on') d.dataset.scanlines = 'on';
  } catch (e) {}
})();
