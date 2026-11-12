// Tuiles « NAS » du tableau de bord: une par disque, plus ventilateurs du NAS, onduleur, RAID, volume.
// Les données viennent de la surveillance du meuble (olh-watchdog), sur le port 27004 du même hôte.
// Les tuiles se réorganisent par glisser-déposer sur leur titre; l'ordre est mémorisé dans ce navigateur.
$(document).ready(function () {
    var base = window.location.protocol + '//' + window.location.hostname + ':27004';
    var STORE = 'nasTilesOrder';
    var colors = {ok: 'text-success', warn: 'text-warning', crit: 'text-danger'};
    var $grid = $('#nas-tiles');

    function savedOrder() {
        try {
            return JSON.parse(localStorage.getItem(STORE)) || [];
        } catch (e) {
            return [];
        }
    }

    function saveOrder() {
        try {
            localStorage.setItem(STORE, JSON.stringify($grid.children('[data-tile]').map(function () {
                return $(this).attr('data-tile');
            }).get()));
        } catch (e) { /* stockage indisponible: l'ordre ne sera pas mémorisé */ }
    }

    function create(t) {
        var $cell = $('<div></div>').attr('data-tile', t.id).addClass('col-md-' + (t.size || 2));
        var $card = $('<div class="card p-3"></div>');
        $card.append('<div class="nas-head"><div class="card-title text-ellipsis nas-handle" title="Glisser pour réorganiser"></div><span class="nas-status"></span></div>');
        $card.append('<div class="stat text-ellipsis"></div>');
        $card.append('<small class="d-block text-ellipsis nas-sub"></small>');
        $card.append('<small class="d-block nas-sub2"></small>');
        $cell.append($card);
        return $cell;
    }

    // Insère une nouvelle tuile à la place mémorisée, ou à la fin si elle est inconnue.
    function place($cell, id) {
        var order = savedOrder();
        var pos = order.indexOf(id);
        if (pos < 0) {
            $grid.append($cell);
            return;
        }
        var placed = false;
        $grid.children('[data-tile]').each(function () {
            var p = order.indexOf($(this).attr('data-tile'));
            if (p < 0 || p > pos) {
                $cell.insertBefore(this);
                placed = true;
                return false;
            }
        });
        if (!placed) {
            $grid.append($cell);
        }
    }

    function apply(t) {
        var $cell = $grid.children('[data-tile]').filter(function () {
            return $(this).attr('data-tile') === t.id;
        });
        if (!$cell.length) {
            $cell = create(t);
            place($cell, t.id);
        }
        $cell.find('.card-title').text(t.title);
        // Charte or et crème: la valeur reste neutre; l'état est porté par un point (normal) ou une pastille avec un mot (à surveiller, critique).
        var level = colors[t.level] ? t.level : 'warn';
        var label = {ok: 'Normal', warn: 'À surveiller', crit: 'Critique'}[level];
        $cell.find('.card').attr('data-level', level);
        $cell.find('.nas-status').attr('data-level', level).text(level === 'ok' ? '' : label)
            .attr('role', level === 'ok' ? 'img' : null).attr('aria-label', level === 'ok' ? label : null).attr('title', label);
        $cell.find('.stat').text(t.value);
        $cell.find('.nas-sub').text(t.sub || '').attr('title', t.sub || '');
        $cell.find('.nas-sub2').text(t.sub2 || '');
    }

    function render(tiles) {
        var present = {};
        $.each(tiles, function (i, t) {
            present[t.id] = true;
            apply(t);
        });
        $grid.children('[data-tile]').each(function () {
            if (!present[$(this).attr('data-tile')]) {
                $(this).remove();
            }
        });
        $('#nas-tiles-msg').text('Glissez les titres pour réorganiser les tuiles (ordre mémorisé dans ce navigateur).');
    }

    function refresh() {
        $.ajax({
            url: base + '/api/tuiles',
            type: 'get',
            dataType: 'json',
            timeout: 4000,
            success: function (result) {
                render(result.tiles || []);
            },
            error: function () {
                $('#nas-tiles-msg').text('Capteurs du NAS indisponibles (surveillance injoignable): les valeurs affichées peuvent être périmées.');
            }
        });
    }

    if ($.fn.sortable) {
        $grid.sortable({
            handle: '.nas-handle',
            tolerance: 'pointer',
            cursor: 'grabbing',
            forceHelperSize: true,
            forcePlaceholderSize: true,
            placeholder: 'nas-placeholder',
            start: function (e, ui) {
                ui.placeholder.addClass(ui.item.attr('class'));
            },
            update: saveOrder
        });
    }

    $('#nas-tiles-reset').on('click', function (e) {
        e.preventDefault();
        try {
            localStorage.removeItem(STORE);
        } catch (err) { /* sans effet */ }
        window.location.reload();
    });

    refresh();
    setInterval(refresh, 10000);
});
