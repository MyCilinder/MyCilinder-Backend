package main

import (
	"fmt"
	"time"
)

type seedVideo struct {
	title, description string
	creator            int
	views, likes       int64
	daysAgo            float64
}

var seedCreators = []string{"Diego Gomez", "Dario Lucciano", "Alexis Flores", "Canal ITESO", "Mariana López"}

var seedVideos = []seedVideo{
	{"Recorrido por el campus del ITESO", "Un recorrido completo por el campus: biblioteca, cafetería y las nuevas aulas.", 3, 15230, 820, 0.2},
	{"Tutorial de Go: tu primer microservicio", "Construimos un servicio HTTP en Go con chi, pruebas y Docker.", 0, 8410, 610, 0.5},
	{"Flutter en 10 minutos", "Introducción rápida a widgets, estado y navegación en Flutter.", 2, 12044, 930, 1},
	{"Cómo funciona HLS", "Explicación de la transmisión adaptativa: manifiestos, segmentos y renditions.", 1, 5320, 300, 1.5},
	{"Atardecer en Guadalajara", "Timelapse del atardecer desde Chapultepec.", 4, 22780, 2100, 2},
	{"Arquitectura en AWS para video bajo demanda", "CloudFront, S3, SQS y ECS Fargate: el diseño de MyCilinder explicado.", 0, 3120, 245, 2.5},
	{"Recetas fáciles: tacos al pastor", "Marinado, trompo casero y salsa. ¡Quedan increíbles!", 4, 40210, 3900, 3},
	{"Partido de fútbol intercarreras", "Resumen del partido Ingeniería vs. Diseño en las canchas del campus.", 3, 9875, 455, 3.5},
	{"Tutorial de Docker Compose", "Levantamos Postgres, Redis y tres servicios con un solo comando.", 1, 6650, 512, 4},
	{"Música en vivo en la explanada", "Concierto acústico del viernes en la explanada principal.", 3, 4300, 380, 5},
	{"Programación concurrente con goroutines", "Canales, select y patrones de concurrencia en Go.", 0, 7021, 590, 6},
	{"Diseño de bases de datos en PostgreSQL", "Índices, llaves foráneas y búsqueda de texto completo en español.", 2, 3890, 210, 7},
	{"Vlog: semana de exámenes", "Cómo sobrevivimos a la semana de exámenes finales.", 4, 11200, 1320, 8},
	{"Introducción a Terraform", "Infraestructura como código: proveedores, estado y módulos.", 1, 2950, 180, 9},
	{"Paseo en bici por el bosque de la Primavera", "Ruta de 20 km por el bosque, con vistas increíbles.", 2, 6120, 740, 10},
	{"Café de especialidad: métodos de extracción", "V60, Chemex y prensa francesa comparados.", 4, 5510, 460, 12},
	{"Charla: escalabilidad para un millón de usuarios", "Lo que cambia cuando tu aplicación pasa de cien a un millón de usuarios.", 0, 9800, 870, 14},
	{"Animación 3D con Blender para principiantes", "Modelado, materiales y una animación sencilla paso a paso.", 1, 4470, 330, 16},
}

var seedComments = []string{
	"¡Excelente video!",
	"Muy bien explicado, gracias.",
	"¿Podrías hacer una segunda parte?",
	"Lo vi dos veces, me encantó.",
	"Justo lo que necesitaba para mi proyecto.",
	"La calidad está increíble 🔥",
}

// seed fills the store with fixed creators, READY videos, and comments. A few creator
// videos are HIDDEN or FAILED so moderation and studio states have data.
func seed(s *Store, now time.Time) {
	creators := make([]string, len(seedCreators))
	for i, name := range seedCreators {
		id := stableID("creator:" + name)
		creators[i] = id
		s.users[id] = &User{ID: id, DisplayName: name}
	}
	for i, sv := range seedVideos {
		id := stableID(fmt.Sprintf("video:%d", i))
		published := now.Add(-time.Duration(sv.daysAgo * float64(24*time.Hour))).Truncate(time.Second)
		s.videos[id] = &Video{
			ID: id, OwnerID: creators[sv.creator], Title: sv.title, Description: sv.description,
			Status: StatusReady, CreatedAt: published.Add(-10 * time.Minute), PublishedAt: &published,
			BaseViews: sv.views, BaseLikes: sv.likes,
		}
		// 0 to 3 comments per video, by other creators.
		for j := 0; j < i%4; j++ {
			c := &Comment{
				ID:        stableID(fmt.Sprintf("comment:%d:%d", i, j)),
				VideoID:   id,
				AuthorID:  creators[(sv.creator+j+1)%len(creators)],
				Body:      seedComments[(i+j)%len(seedComments)],
				CreatedAt: published.Add(time.Duration(j+1) * time.Hour),
			}
			s.comments[id] = append(s.comments[id], c)
			s.byID[c.ID] = c
		}
	}
	hiddenAt := now.Add(-20 * 24 * time.Hour).Truncate(time.Second)
	s.videos[stableID("video:hidden")] = &Video{
		ID: stableID("video:hidden"), OwnerID: creators[1], Title: "Video oculto por moderación",
		Description: "Solo su creador lo ve en el estudio.", Status: StatusHidden,
		CreatedAt: hiddenAt, PublishedAt: &hiddenAt, BaseViews: 120,
	}
}

// seedForUser gives a real (Cognito) user a few videos of their own on first sight, so
// the creator studio has every state to show.
func (s *Store) seedForUser(sub string, now time.Time) {
	if s.seeded[sub] {
		return
	}
	s.seeded[sub] = true
	reason := "El video dura más de 10 minutos (máximo 600 s)."
	items := []struct {
		title, desc, status string
		ago                 time.Duration
		views, likes        int64
	}{
		{"Mi primer video en MyCilinder", "Probando la plataforma.", StatusReady, 72 * time.Hour, 342, 28},
		{"Mi proyecto de Sistemas Escalables", "Demo del proyecto final.", StatusReady, 30 * time.Hour, 1280, 95},
		{"Video en procesamiento", "Se está transcodificando.", StatusProcessing, 5 * time.Minute, 0, 0},
		{"Clase completa grabada", "Grabación de dos horas.", StatusFailed, 6 * time.Hour, 0, 0},
		{"Video ocultado por un operador", "Ocultado tras un reporte.", StatusHidden, 10 * 24 * time.Hour, 57, 3},
	}
	for i, it := range items {
		id := stableID(fmt.Sprintf("user:%s:%d", sub, i))
		created := now.Add(-it.ago).Truncate(time.Second)
		v := &Video{
			ID: id, OwnerID: sub, Title: it.title, Description: it.desc, Status: it.status,
			CreatedAt: created, BaseViews: it.views, BaseLikes: it.likes,
		}
		switch it.status {
		case StatusReady, StatusHidden:
			p := created.Add(3 * time.Minute)
			v.PublishedAt = &p
		case StatusFailed:
			v.FailureReason = &reason
		}
		s.videos[id] = v
	}
}
