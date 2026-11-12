// Réorganisation du tableau de bord d'OpenLinkHub par glisser-déposer.
// OpenLinkHub construit les cartes de ses appareils (ventilateurs, sondes…) une fois, regroupées par appareil.
// Ce script les réunit dans une seule grille, que l'on réordonne en glissant l'en-tête d'une carte.
// L'ordre est mémorisé dans ce navigateur (localStorage), pas sur le NAS.
$(document).ready(function () {
    var STORE = 'dashboardCardsOrder';

    function savedOrder() {
        try {
            return JSON.parse(localStorage.getItem(STORE)) || [];
        } catch (e) {
            return [];
        }
    }

    function saveOrder($grid) {
        try {
            localStorage.setItem(STORE, JSON.stringify($grid.children().map(function () {
                return $(this).attr('data-card');
            }).get()));
        } catch (e) { /* stockage indisponible */ }
    }

    // Clé stable d'une carte: premier identifiant qu'elle contient (ex. speed-<série>-<canal>),
    // sinon le titre de son en-tête.
    function keyOf($card) {
        var id = $card.find('[id]').first().attr('id');
        return id ? id : $card.find('.header-left').first().text().trim();
    }

    function build() {
        var $host = $('#system-cards');
        if ($host.children('#device-grid').length) {
            return;
        }
        var $cards = $host.find('.row > [class^="col-md-"]');
        if (!$cards.length) {
            return;
        }
        var order = savedOrder();
        var items = $cards.get().map(function (el, i) {
            var key = keyOf($(el));
            var pos = order.indexOf(key);
            return {el: el, key: key, rank: pos < 0 ? 1000000 + i : pos};
        });
        items.sort(function (a, b) {
            return a.rank - b.rank;
        });

        var $grid = $('<div id="device-grid" class="row g-4 mb-4 align-items-start"></div>');
        $.each(items, function (i, it) {
            $(it.el).attr('data-card', it.key);
            $grid.append(it.el);
        });
        $host.find('.row').not($grid).remove(); // anciennes rangées, désormais vides
        $host.prepend($grid);

        if ($.fn.sortable) {
            $grid.sortable({
                handle: '.card-header',
                tolerance: 'pointer',
                cursor: 'grabbing',
                forcePlaceholderSize: true,
                placeholder: 'nas-placeholder',
                start: function (e, ui) {
                    ui.placeholder.addClass(ui.item.attr('class'));
                },
                update: function () {
                    saveOrder($grid);
                }
            });
            $grid.find('.card-header').css('cursor', 'grab').attr('title', 'Glisser pour réorganiser');
        }
    }

    // Les cartes arrivent par Ajax: OpenLinkHub ajoute la classe « ready » quand elles sont toutes en place
    // (et la retire puis la remet quand on ajoute ou retire un appareil du tableau de bord).
    var host = document.getElementById('system-cards');
    if (!host) {
        return;
    }
    var observer = new MutationObserver(function () {
        if ($(host).hasClass('ready')) {
            build();
        }
    });
    observer.observe(host, {attributes: true, attributeFilter: ['class']});
    if ($(host).hasClass('ready')) {
        build();
    }

    // Style du repère de dépôt, commun aux deux grilles
    if (!$('#nas-placeholder-style').length) {
        $('head').append('<style id="nas-placeholder-style">' +
            '.nas-placeholder{border:1px dashed rgba(128,128,128,.7);border-radius:8px;min-height:90px;visibility:visible !important;}' +
            '.nas-handle{cursor:grab;}</style>');
    }

    // Lien de remise à zéro (partagé avec les tuiles NAS: efface les deux ordres)
    $('#nas-tiles-reset').on('click', function () {
        try {
            localStorage.removeItem(STORE);
        } catch (e) { /* sans effet */ }
    });
});
