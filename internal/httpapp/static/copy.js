(function () {
  document.addEventListener("click", function (event) {
    var button = event.target.closest("button[data-copy]");
    if (!button) {
      return;
    }
    var text = button.getAttribute("data-copy");
    if (!text || !navigator.clipboard) {
      return;
    }
    var label = button.textContent;
    navigator.clipboard.writeText(text).then(function () {
      button.textContent = "Skopiowane";
      window.setTimeout(function () {
        button.textContent = label;
      }, 1500);
    });
  });
})();
