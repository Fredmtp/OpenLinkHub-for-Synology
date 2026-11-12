// Thème or et crème d'OpenLinkHub: clair par défaut, sombre au choix. Le choix est mémorisé dans ce navigateur (localStorage).
// Chargé en tête de page (head.html) pour appliquer le thème AVANT l'affichage: pas d'éclair de la mauvaise couleur.
// Le bouton #goldcreamThemeToggle (menu latéral et page des graphiques) bascule et prévient les pages par l'événement « goldcreamtheme ».
(function () {
    'use strict';
    var KEY = 'goldcreamTheme';
    var SOLEIL = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"><circle cx="12" cy="12" r="4"/><path d="M12 2.5v2.2M12 19.3v2.2M2.5 12h2.2M19.3 12h2.2M5.3 5.3l1.6 1.6M17.1 17.1l1.6 1.6M18.7 5.3l-1.6 1.6M6.9 17.1l-1.6 1.6"/></svg>';
    var LUNE = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M20 14.5A8.5 8.5 0 1 1 9.5 4a6.8 6.8 0 0 0 10.5 10.5z"/></svg>';

    function lire() {
        try {
            var t = localStorage.getItem(KEY);
            return (t === 'dark' || t === 'light') ? t : 'light';
        } catch (e) {
            return 'light';
        }
    }

    function appliquer(t) {
        document.documentElement.setAttribute('data-theme', t);
        var m = document.querySelector('meta[name="color-scheme"]');
        if (m) { m.setAttribute('content', t); }
    }

    appliquer(lire());

    document.addEventListener('DOMContentLoaded', function () {
        var b = document.getElementById('goldcreamThemeToggle');
        if (!b) { return; }

        function majBouton() {
            var sombre = document.documentElement.getAttribute('data-theme') === 'dark';
            b.setAttribute('aria-label', sombre ? 'Passer au thème clair' : 'Passer au thème sombre');
            b.setAttribute('title', sombre ? 'Passer au thème clair' : 'Passer au thème sombre');
            var l = b.querySelector('.goldcream-theme-label');
            var i = b.querySelector('.goldcream-theme-icon');
            if (l) { l.textContent = sombre ? 'Thème clair' : 'Thème sombre'; }
            if (i) { i.innerHTML = sombre ? SOLEIL : LUNE; }
        }

        b.addEventListener('click', function () {
            var nouveau = document.documentElement.getAttribute('data-theme') === 'dark' ? 'light' : 'dark';
            try { localStorage.setItem(KEY, nouveau); } catch (e) { /* stockage indisponible: le choix vaut pour cette page seulement */ }
            appliquer(nouveau);
            majBouton();
            window.dispatchEvent(new CustomEvent('goldcreamtheme', {detail: nouveau}));
        });
        majBouton();
    });
})();
