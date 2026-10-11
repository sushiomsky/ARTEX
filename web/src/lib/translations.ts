import en from './locales/en.json';
import es from './locales/es.json';
import fr from './locales/fr.json';

type Locale = 'en' | 'es' | 'fr';

const translations: Record<Locale, typeof en> = {
  en,
  es,
  fr,
};

export function t(key: string, locale: Locale = 'en'): string {
  const localeTranslations = translations[locale];
  if (!localeTranslations) return key;
  
  const keys = key.split('.');
  let value: unknown = localeTranslations;
  
  for (const k of keys) {
    if (typeof value !== 'object' || value === null) return key;
    value = (value as Record<string, unknown>)[k];
  }
  
  return typeof value === 'string' ? value : key;
}