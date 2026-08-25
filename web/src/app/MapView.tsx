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

/** Leaflet map with license-plate price markers. Also doubles as a location
 * picker for hosts when onPick is set. */
export function MapView({ cars, center, zoom = 13, onSelect, onPick, pin, className = '' }: {
  readonly cars: readonly MapCar[];
  readonly center: readonly [number, number];
  readonly zoom?: number;
  readonly onSelect?: (id: string) => void;
  readonly onPick?: (lat: number, lng: number) => void;
  /** Host-mode dropped pin. */
  readonly pin?: readonly [number, number] | null;
  readonly className?: string;
}) {
  const container = useRef<HTMLDivElement>(null);
  const map = useRef<L.Map>(null);
  const layer = useRef<L.LayerGroup>(null);
  const pickHandler = useRef(onPick);
  const selectHandler = useRef(onSelect);
  pickHandler.current = onPick;
  selectHandler.current = onSelect;

  useEffect(() => {
    if (!container.current || map.current) {
      return;
    }
    const created = L.map(container.current, { zoomControl: true, attributionControl: true });
    created.setView([center[0], center[1]], zoom);
    L.tileLayer('https://{s}.basemaps.cartocdn.com/light_all/{z}/{x}/{y}{r}.png', {
      attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OSM</a> &copy; <a href="https://carto.com/attributions">CARTO</a>',
      maxZoom: 19,
    }).addTo(created);
    created.on('click', (event: L.LeafletMouseEvent) => {
      pickHandler.current?.(event.latlng.lat, event.latlng.lng);
    });
    map.current = created;
    layer.current = L.layerGroup().addTo(created);
    return () => {
      created.remove();
      map.current = null;
      layer.current = null;
    };
    // The map mounts once; markers and the pin update in the effect below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

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
          html: `<div class="plate ${car.selected ? 'bg-pine-600 text-paper-50' : 'bg-paper-50 text-paper-900'} cursor-pointer text-sm shadow-card">${money(car.priceCents)}</div>`,
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
