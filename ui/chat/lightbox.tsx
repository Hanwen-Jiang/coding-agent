import React, {useEffect} from 'react';
import {Icon} from './icon';

export function ImageLightbox({
 src,
 alt,
 closeLabel,
 onClose,
}: {
 src: string;
 alt?: string;
 closeLabel?: string;
 onClose: () => void;
}) {
 useEffect(() => {
  const onKey = (event: KeyboardEvent) => {
   if (event.key === 'Escape') {
    event.preventDefault();
    onClose();
   }
  };
  window.addEventListener('keydown', onKey);
  return () => window.removeEventListener('keydown', onKey);
 }, [onClose]);

 return (
  <div
   className="image-lightbox-backdrop"
   role="dialog"
   aria-modal="true"
   aria-label={alt || closeLabel || 'Image preview'}
   onClick={event => {
    if (event.target === event.currentTarget) onClose();
   }}
  >
   <div className="image-lightbox-dialog">
    <button
     type="button"
     className="btn ghost sm icon-only image-lightbox-close"
     aria-label={closeLabel || 'Close'}
     onClick={onClose}
    >
     <Icon name="x" size="sm" />
    </button>
    <img className="image-lightbox-img" src={src} alt={alt || ''} />
    {alt && <div className="image-lightbox-caption">{alt}</div>}
   </div>
  </div>
 );
}
