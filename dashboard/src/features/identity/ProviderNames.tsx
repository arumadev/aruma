import styles from './ProviderNames.module.css';

/**
 * A user's sign-in methods, one per line. Joining them with "and" would be ambiguous: "Email
 * and password" is itself one method.
 */
export function ProviderNames({ names }: { names: string[] }) {
  return (
    <ul className={styles.list}>
      {names.map((n) => (
        <li key={n}>{n}</li>
      ))}
    </ul>
  );
}
