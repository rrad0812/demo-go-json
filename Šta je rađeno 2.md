# Šta je rađeno II deo

Odlično pitanje. I iskreno, ne moraš odmah na React.

Najvažnije: React i Vite nisu skupo rešenje po sebi. Frontend može da hostuješ besplatno (Vercel, Netlify, Cloudflare Pages), a trošak ti i dalje uglavnom ostaje backend + baza.

Ako ti je cilj najjeftinije i najbrže da pokažeš šta radi:

1. Zadrži postojeći Go API kako jeste.
2. Napravi ultra-jednostavan frontend (par stranica i fetch pozivi).
3. Tek kad osetiš bol (state, routing, više ekrana), pređi na React + Vite.

Što se W2UI tiče, nije loš za admin-style aplikacije, ali ima realne minuse:

1. Manji ekosistem i manje modernih primera.
2. Teže dugoročno održavanje i manje ljudi ga zna.
3. Slabiji fit za moderni SPA tok i TypeScript praksu.

Zato sam imao zamerke više zbog dugoročnosti, ne zato što je “loš”.

Kako da odlučiš šta je najbolje za tebe:

1. Ako hoćeš brz admin ekran i ne planiraš “pravi proizvod”: W2UI može.
2. Ako hoćeš da učiš nešto što ćeš koristiti i za druge projekte: React + Vite.
3. Ako hoćeš minimum complexity sada: vanila frontend prvo, pa React kasnije.

Najpraktičniji start za tebe (bez stresa):

1. Nedelja 1: napravi login ekran + lista jednog modula kao čist frontend koji zove tvoj API.
2. Nedelja 2: dodaj create/edit formu i jednu detail stranu.
3. Kad to radi, proceni: ako kod postaje haotičan, tada migracija na React + Vite.

Dakle: React + Vite nije skuplje, ali jeste veći ulazak. Za tebe je najpametnije da kreneš malim korakom, pa tek onda odlučiš da li ostaješ na jednostavnom UI ili prelaziš na React.

Ako hoćeš, mogu sledeće da ti dam potpuno konkretno:

1. Plan vanila UI za 1 vikend.
2. Plan React + Vite za 1 vikend.
3. Poređenje po vremenu, težini i trošku, pa da izabereš bez nagađanja.

---

Super izbor, sada ću ti dati konkretan početni plan baš za React + Vite, prilagođen tvom Go API-ju i bez nepotrebne teorije.

Najbolji početak za tebe je da napraviš mali frontend koji pokriva samo 4 stvari:

1. Login
2. Provera sesije
3. Lista modula
4. Lista zapisa za jedan modul

To je dovoljno da odmah vidiš rezultat i da učiš kroz realan tok.

Evo tačno kako da kreneš.

1. Napravi frontend projekat
   Pokreni ove komande:

   ```sh
   npm create vite@latest frontend -- --template react  
   cd frontend  
   npm install  
   npm install react-router-dom axios
   ```

2. Pokreni frontend lokalno

   ```sh
   npm run dev
   ```

3. Poveži ga sa Go API-jem
   U frontend projektu dodaj env promenljivu za API bazu, sa vrednošću:

   ```sh
   VITE_API_BASE_URL=<http://localhost:8080>
   ```

4. Auth tok koji treba da ispratiš
   Pošto backend radi sa session cookie + CSRF, frontend treba ovako:
   1. Login ekran šalje POST /login sa username i password.
   2. Iz login odgovora uzima csrf_token i čuva ga u memoriji aplikacije.
   3. Za svaki POST, PUT, DELETE šalje header X-CSRF-Token.
   4. Na startu aplikacije zove GET /auth/session da vidi da li je korisnik već ulogovan.
   5. Ako dobije 403 zbog CSRF, može jednom da pozove POST /auth/csrf/refresh i ponovi zahtev.

5. Struktura ekrana za prvi sprint
   1. Login page
   2. Dashboard page sa listom modula
   3. Module records page
   4. Jednostavan create form page

6. Šta da ne radiš na početku
   1. Nemoj odmah state management biblioteke.
   2. Nemoj odmah TypeScript ako ti je previše u startu.
   3. Nemoj odmah komplikovan UI framework.

7. Najjeftiniji hosting model
   1. Frontend na Vercel ili Netlify free planu
   2. Backend gde već budeš držao Go API
   3. To znači da React + Vite nije dodatni veliki trošak

Ako želiš, sledeći korak mogu odmah da uradim praktično:

1. Da ti u ovom workspace-u napravim frontend folder sa React + Vite skeletonom
2. Da odmah ubacim gotov login i session bootstrap prema tvom API-ju

Samo potvrdi i krećem sa kodom.

---
