import { useEffect, useRef } from 'react';
import L from 'leaflet';
import 'leaflet/dist/leaflet.css';
import { money } from './format';

export type MapCar = {
  readonly id: string;
  readonly lat: number;
  readonly lng: number;
  readonly priceCents: number;
  readonly selected?: boolean;
};

/**
 * Required for fair, non-commercial use. Allows up to 5M requests / month. If we need more than
 * that, can always host the maps with our own OSM service or something.
 * See: https://carto.com/basemaps/apikey/
 */
const CARTO_CDN_KEY = 'cb1_29ij_1_6eb87ea2754516a5035c7fa9';

/** Leaflet map with license-plate price markers. Also doubles as a location
 * picker for hosts when onPick is set. */
export function MapView({ cars, center, zoom = 13, onSelect, onPick, onMoved, pin, className = '' }: {
  readonly cars: readonly MapCar[];
  readonly center: readonly [number, number];
  readonly zoom?: number;
  readonly onSelect?: (id: string) => void;
  readonly onPick?: (lat: number, lng: number) => void;
  /** Fires with the new center after the user drags or zooms the map. */
  readonly onMoved?: (lat: number, lng: number) => void;
  /** Host-mode dropped pin. */
  readonly pin?: readonly [number, number] | null;
  readonly className?: string;
}) {
  const container = useRef<HTMLDivElement>(null);
  const map = useRef<L.Map>(null);
  const layer = useRef<L.LayerGroup>(null);
  const programmaticMove = useRef(false);
  const pickHandler = useRef(onPick);
  const selectHandler = useRef(onSelect);
  const movedHandler = useRef(onMoved);
  pickHandler.current = onPick;
  selectHandler.current = onSelect;
  movedHandler.current = onMoved;

  useEffect(() => {
    if (!container.current || map.current) {
      return;
    }
    const created = L.map(container.current, { zoomControl: true, attributionControl: true });
    created.setView([center[0], center[1]], zoom);
    L.tileLayer(`https://{s}.basemaps.cartocdn.com/light_all/{z}/{x}/{y}{r}.png?key=${CARTO_CDN_KEY}`, {
      attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OSM</a> &copy; <a href="https://carto.com/attributions">CARTO</a>',
      maxZoom: 19,
    }).addTo(created);
    created.on('click', (event: L.LeafletMouseEvent) => {
      pickHandler.current?.(event.latlng.lat, event.latlng.lng);
    });
    created.on('moveend', () => {
      if (programmaticMove.current) {
        programmaticMove.current = false;
        return;
      }
      const at = created.getCenter();
      movedHandler.current?.(at.lat, at.lng);
    });
    map.current = created;
    layer.current = L.layerGroup().addTo(created);
    // The pane resizes when the hero collapses; Leaflet only redraws tiles
    // for the new size when told. invalidateSize fires moveend even without
    // panning, so guard it or a resize fakes a user drag. The fire is
    // synchronous (no animation), which lets the flag reset right after.
    const observer = new ResizeObserver(() => {
      programmaticMove.current = true;
      created.invalidateSize({ pan: false });
      programmaticMove.current = false;
    });
    observer.observe(container.current);
    return () => {
      observer.disconnect();
      created.remove();
      map.current = null;
      layer.current = null;
    };
    // The map mounts once; markers and the pin update in the effect below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Follow the center prop without tearing the map down. Two guards keep the
  // user in charge: the effect keys on the coordinates, not the array's
  // identity (which changes every render and used to snap a dragged map
  // back), and a no-op recenter is skipped so adopting the dragged center via
  // Search this area leaves the view exactly where the user put it.
  const centerLat = center[0];
  const centerLng = center[1];
  useEffect(() => {
    const current = map.current;
    if (!current) {
      return;
    }
    const at = current.getCenter();
    if (Math.abs(at.lat - centerLat) < 1e-6 && Math.abs(at.lng - centerLng) < 1e-6) {
      return;
    }
    programmaticMove.current = true;
    current.setView([centerLat, centerLng], current.getZoom());
  }, [centerLat, centerLng]);

  useEffect(() => {
    const group = layer.current;
    if (!group) {
      return;
    }
    group.clearLayers();
    for (const car of cars) {
      const marker = L.marker([car.lat, car.lng], {
        icon: L.divIcon({
          className: '',
          html: `<div class="plate ${car.selected ? 'bg-pine-600 text-paper-50 hover:bg-pine-700 active:bg-pine-800' : 'bg-paper-50 text-paper-900 hover:bg-paper-200 active:bg-paper-300'} cursor-pointer text-sm shadow-card transition-colors duration-75">${money(car.priceCents)}</div>`,
          iconSize: undefined,
          iconAnchor: [24, 14],
        }),
      });
      marker.on('click', () => selectHandler.current?.(car.id));
      group.addLayer(marker);
    }
    if (pin) {
      group.addLayer(
        L.marker([pin[0], pin[1]], {
          icon: L.divIcon({
            className: '',
            html: '<div class="plate bg-marigold-100 text-marigold-700 text-sm">here</div>',
            iconAnchor: [20, 14],
          }),
        }),
      );
    }
  }, [cars, pin]);

  return <div ref={container} className={`z-0 ${className}`} />;
}
